package regio

import (
	"os"
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
