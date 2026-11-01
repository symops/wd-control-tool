package regio

import (
	"os"
	"sync"
	"testing"
)

func newFakeMem(t *testing.T, size int64) *Mem {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "regio-test-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return OpenFile(f)
}

func TestReadWriteReg32_RoundTrip(t *testing.T) {
	mem := newFakeMem(t, 1<<20)
	if err := mem.WriteReg32(0x1000, 0xDEADBEEF); err != nil {
		t.Fatalf("WriteReg32: %v", err)
	}
	got, err := mem.ReadReg32(0x1000)
	if err != nil {
		t.Fatalf("ReadReg32: %v", err)
	}
	if got != 0xDEADBEEF {
		t.Errorf("got 0x%x, want 0xDEADBEEF", got)
	}
}

func TestSetClearBits(t *testing.T) {
	mem := newFakeMem(t, 1<<20)
	if err := mem.WriteReg32(0x2000, 0x0F0F0F0F); err != nil {
		t.Fatalf("WriteReg32: %v", err)
	}
	if err := mem.SetClearBits(0x2000, 0xF0, 0x0F); err != nil {
		t.Fatalf("SetClearBits: %v", err)
	}
	got, _ := mem.ReadReg32(0x2000)
	want := uint32(0x0F0F0FF0) // low byte: 0x0F with 0x0F cleared and 0xF0 set = 0xF0
	if got != want {
		t.Errorf("got 0x%08x, want 0x%08x", got, want)
	}
}

// A register address can legitimately sit close enough to the end of
// a page that its 4 bytes straddle into the next page - nothing
// guarantees every physical register of interest is far from a page
// boundary. Confirm ReadReg32/WriteReg32 handle that instead of
// panicking on an out-of-bounds slice index into a single
// pageSize-sized mmap.
func TestReadWriteReg32_PageBoundaryStraddle(t *testing.T) {
	pageSize := int64(os.Getpagesize())
	mem := newFakeMem(t, pageSize*4)

	// Last 2 bytes of a page: the 4-byte register spans into the next
	// page.
	addr := pageSize*2 - 2

	if err := mem.WriteReg32(addr, 0xAABBCCDD); err != nil {
		t.Fatalf("WriteReg32 at page boundary (0x%x): %v", addr, err)
	}
	got, err := mem.ReadReg32(addr)
	if err != nil {
		t.Fatalf("ReadReg32 at page boundary (0x%x): %v", addr, err)
	}
	if got != 0xAABBCCDD {
		t.Errorf("got 0x%08x, want 0xAABBCCDD", got)
	}
}

func TestReadWriteReg32_LastBytesOfFile(t *testing.T) {
	// addr+4 must not run past the end of the backing file/device
	// either - exercise the very last 4 bytes of a non-page-aligned
	// file size.
	pageSize := int64(os.Getpagesize())
	size := pageSize*3 + 100
	mem := newFakeMem(t, size)

	addr := size - 4
	if err := mem.WriteReg32(addr, 0x12345678); err != nil {
		t.Fatalf("WriteReg32 at EOF-4 (0x%x): %v", addr, err)
	}
	got, err := mem.ReadReg32(addr)
	if err != nil {
		t.Fatalf("ReadReg32 at EOF-4 (0x%x): %v", addr, err)
	}
	if got != 0x12345678 {
		t.Errorf("got 0x%08x, want 0x12345678", got)
	}
}

// TestApplyAll_StopsAtFirstError (below) only exercises ReadReg32's
// mmap-failure path, since SetClearBits always reads before it
// writes - WriteReg32's own error return was never hit by anything.
// Call it directly on a closed file to close that gap.
func TestWriteReg32_MmapFailure(t *testing.T) {
	mem := newFakeMem(t, 1<<20)
	if err := mem.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := mem.WriteReg32(0x1000, 0xDEADBEEF); err == nil {
		t.Fatal("WriteReg32 on a closed file: want error, got nil")
	}
}

func TestApplyAll_StopsAtFirstError(t *testing.T) {
	mem := newFakeMem(t, 1<<20)
	if err := mem.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// mem's backing file is now closed - every Apply must fail.
	err := mem.ApplyAll([]Write{
		{Addr: 0x1000, Set: 1},
		{Addr: 0x2000, Set: 1},
	})
	if err == nil {
		t.Fatal("ApplyAll on a closed file: want error, got nil")
	}
}

func TestApplyAll_Empty(t *testing.T) {
	mem := newFakeMem(t, 1<<20)
	if err := mem.ApplyAll(nil); err != nil {
		t.Errorf("ApplyAll(nil): %v", err)
	}
	if err := mem.ApplyAll([]Write{}); err != nil {
		t.Errorf("ApplyAll([]Write{}): %v", err)
	}
}

func TestPageAlign(t *testing.T) {
	cases := []struct {
		addr, pageSize int64
		wantBase       int64
		wantOff        int64
	}{
		{0x1000, 4096, 0x1000, 0},
		{0x1004, 4096, 0x1000, 4},
		{0xF1010100, 4096, 0xF1010000, 0x100},
		{4096 - 2, 4096, 0, 4094},
	}
	for _, c := range cases {
		base, off := pageAlign(c.addr, c.pageSize)
		if base != c.wantBase || off != c.wantOff {
			t.Errorf("pageAlign(0x%x, %d) = (0x%x, %d), want (0x%x, %d)", c.addr, c.pageSize, base, off, c.wantBase, c.wantOff)
		}
	}
}

// Mem's doc comment claims every method (Close included) serializes
// via its internal mutex, making concurrent use from multiple
// goroutines safe. Confirm that under `go test -race`: many
// goroutines hammering WriteReg32/ReadReg32 on a handful of shared
// addresses, with a GENUINELY concurrent Close() racing against
// them (not one run only after everything else has already
// finished, which an earlier version of this test did despite this
// comment's own claim otherwise - that version could never have
// caught a regression in Close()'s own locking at all, since nothing
// was left running for Close() to race against by the time it ran).
// If Close ever stopped taking the lock, -race would flag the close
// racing against an in-flight mmap/write; once Close() does win the
// race, every later Read/Write/SetClearBits legitimately starts
// failing (the file is gone) - that's expected and not itself a
// failure, so only a genuine race (caught by -race) or a panic fails
// this test, not an individual operation's error return.
func TestMem_ConcurrentAccessIsRaceFree(t *testing.T) {
	mem := newFakeMem(t, 1<<20)

	addrs := []int64{0x1000, 0x2000, 0x3000, 0x4000}
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			addr := addrs[i%len(addrs)]
			if err := mem.WriteReg32(addr, uint32(i)); err != nil {
				return // expected once Close() has won the race - not a failure
			}
			if _, err := mem.ReadReg32(addr); err != nil {
				return
			}
			_ = mem.SetClearBits(addr, 0x1, 0x2)
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := mem.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	wg.Wait()
}

// SetClearBits does ReadReg32 then WriteReg32 - if each merely took
// mu independently (released between the two), two concurrent
// SetClearBits calls on the SAME address could still interleave:
// both read the same starting value before either writes, so the
// second write silently overwrites the first's bit (a classic
// read-modify-write lost update) even though neither individual
// Read/Write call ever raced in the `go test -race` sense. Have N
// goroutines each set one DISTINCT bit on a shared register with no
// artificial delay - real syscall overhead in mapRegister's
// mmap/munmap is enough to open the window on a multi-core machine -
// and confirm every single bit survives.
func TestMem_SetClearBits_ConcurrentDistinctBitsAllSurvive(t *testing.T) {
	mem := newFakeMem(t, 1<<20)
	const addr = 0x5000
	const n = 32

	var wg sync.WaitGroup
	wg.Add(n)
	for bit := 0; bit < n; bit++ {
		go func(bit uint32) {
			defer wg.Done()
			if err := mem.SetClearBits(addr, 1<<bit, 0); err != nil {
				t.Errorf("SetClearBits(bit %d): %v", bit, err)
			}
		}(uint32(bit))
	}
	wg.Wait()

	got, err := mem.ReadReg32(addr)
	if err != nil {
		t.Fatalf("ReadReg32: %v", err)
	}
	if got != 0xFFFFFFFF {
		t.Errorf("after %d goroutines each setting one distinct bit: got 0x%08x, want 0xFFFFFFFF (missing bits: 0x%08x - SetClearBits' read-modify-write wasn't atomic)", n, got, ^got)
	}
}

// Different property from TestMem_ConcurrentAccessIsRaceFree (which
// only checks `go test -race` sees no unsynchronized memory access):
// WriteReg32 writes its 4 bytes one field assignment at a time
// (data[off], data[off+1], ...), so if its lock were ever held for
// less than the whole operation, a concurrent writer to the SAME
// address could observe (or produce) a "torn" value - some bytes
// from one writer's value, some from another's - even though -race
// might not flag byte-level data[i] = x assignments to the same mmap
// region as a race the way it would a Go-level variable (this is
// physical memory through a byte slice, not race-detector-
// instrumented Go memory in the usual sense for every access
// pattern). Confirm directly: many goroutines repeatedly write their
// own distinctive, internally-uniform 4-byte pattern (0x01010101,
// 0x02020202, ...) to the same address; the final value read back
// must be EXACTLY one writer's clean pattern, never a mix.
func TestMem_WriteReg32_ConcurrentWritesNeverTorn(t *testing.T) {
	mem := newFakeMem(t, 1<<20)
	const addr = 0x6000
	const writers = 9

	valid := make(map[uint32]bool, writers)
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 1; i <= writers; i++ {
		v := uint32(i) * 0x01010101
		valid[v] = true
		go func(v uint32) {
			defer wg.Done()
			for round := 0; round < 200; round++ {
				if err := mem.WriteReg32(addr, v); err != nil {
					t.Errorf("WriteReg32: %v", err)
					return
				}
			}
		}(v)
	}
	wg.Wait()

	got, err := mem.ReadReg32(addr)
	if err != nil {
		t.Fatalf("ReadReg32: %v", err)
	}
	if !valid[got] {
		t.Errorf("after concurrent writes: got 0x%08x, want one of the %d clean writer patterns - this value is torn (mixed bytes from more than one writer)", got, writers)
	}
}

// Same property, but for a reader running DURING active writes, not
// just the one read after everything settles - both ReadReg32 and
// WriteReg32 take the same mutex, so a reader should never be able
// to observe a torn value mid-write either; this is the read-side
// counterpart to TestMem_WriteReg32_ConcurrentWritesNeverTorn.
func TestMem_ReadReg32_NeverObservesATornValueDuringConcurrentWrites(t *testing.T) {
	mem := newFakeMem(t, 1<<20)
	const addr = 0x7000
	const writers = 9

	valid := make(map[uint32]bool, writers)
	for i := 1; i <= writers; i++ {
		valid[uint32(i)*0x01010101] = true
	}
	if err := mem.WriteReg32(addr, 0x01010101); err != nil { // seed a valid starting value
		t.Fatalf("seed WriteReg32: %v", err)
	}

	// Writers run until told to stop (via stopWriters, closed only
	// AFTER every reader below has finished) - started first so
	// they're already actively writing once the readers begin.
	stopWriters := make(chan struct{})
	var writersWG sync.WaitGroup
	writersWG.Add(writers)
	for i := 1; i <= writers; i++ {
		v := uint32(i) * 0x01010101
		go func(v uint32) {
			defer writersWG.Done()
			for {
				select {
				case <-stopWriters:
					return
				default:
					if err := mem.WriteReg32(addr, v); err != nil {
						t.Errorf("WriteReg32: %v", err)
						return
					}
				}
			}
		}(v)
	}

	const readers = 4
	var readersWG sync.WaitGroup
	readersWG.Add(readers)
	for r := 0; r < readers; r++ {
		go func() {
			defer readersWG.Done()
			for i := 0; i < 500; i++ {
				got, err := mem.ReadReg32(addr)
				if err != nil {
					t.Errorf("ReadReg32: %v", err)
					return
				}
				if !valid[got] {
					t.Errorf("ReadReg32 observed a torn value 0x%08x (not one of the %d clean writer patterns) while writes were in progress", got, writers)
					return
				}
			}
		}()
	}

	readersWG.Wait()   // readers run their fixed iterations WHILE writers are still actively writing
	close(stopWriters) // only now tell the writers to stop
	writersWG.Wait()
}
