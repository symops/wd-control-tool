package mcu

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/test/fakemcu"
)

// sendBytes' write-error branch had no coverage at all (confirmed via
// `go test -coverpkg`) - every other test sends into a live, open pty
// that always accepts the write. Close the underlying file out from
// under the Port first, so the very first byte's Write fails, and
// confirm the error is propagated (wrapped with context) rather than
// silently swallowed.
func TestPort_SendBytes_WriteErrorPropagates(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)
	fake.Slave.Close() // underlying fd now invalid for Write

	err := p.sendBytes([]byte{0xAA, 0xBB, 0xCC}, nil)
	if err == nil {
		t.Fatal("sendBytes on a closed file: want error, got nil")
	}
	if !strings.Contains(err.Error(), "mcu: write:") {
		t.Errorf("sendBytes error = %q, want it wrapped with \"mcu: write:\" context", err.Error())
	}
}

// Port.sendBytes holds mu for the whole frame specifically so two
// goroutines sending on the same Port can't interleave their bytes
// mid-frame on the wire - a logical correctness property `go test
// -race` alone can't verify (a mutex can make memory accesses
// race-free while still letting two frames' bytes interleave in
// whatever order the scheduler happens to run Write calls in, if the
// lock were held per-byte instead of per-frame). Confirm it directly:
// two goroutines repeatedly send two different, recognizable 7-byte
// frames on the same Port; every 7-byte run that arrives on the wire
// must be entirely one frame or entirely the other, never a mix.
func TestPort_SendBytes_ConcurrentFramesNeverInterleave(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)

	// 500, not the original 25: an interleaving bug (if the per-frame
	// lock ever regressed to per-byte) is a scheduling-timing hit, not
	// a guaranteed reproduction on the first few frames - more rounds
	// means more chances for the scheduler to interrupt mid-frame.
	const rounds = 500
	frameA := []byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}
	frameB := []byte{0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB}

	var wg sync.WaitGroup
	sendErrs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := p.sendBytes(frameA, nil); err != nil {
				sendErrs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := p.sendBytes(frameB, nil); err != nil {
				sendErrs <- err
				return
			}
		}
	}()

	total := rounds * 2 * len(frameA)
	got := fake.ReadFrame(t, total, 5*time.Second)

	wg.Wait()
	close(sendErrs)
	for err := range sendErrs {
		t.Fatalf("sendBytes: %v", err)
	}

	for i := 0; i < len(got); i += 7 {
		chunk := got[i : i+7]
		allA, allB := true, true
		for _, b := range chunk {
			if b != 0xAA {
				allA = false
			}
			if b != 0xBB {
				allB = false
			}
		}
		if !allA && !allB {
			t.Fatalf("byte run at offset %d is neither all-0xAA nor all-0xBB (interleaved frames): % X", i, chunk)
		}
	}
}

// Port.Close takes mu like every other method, so it should be safe
// to race against in-flight sendBytes calls the same way
// regio.Mem.Close is (see pkg/regio/regio_test.go's
// TestMem_ConcurrentAccessIsRaceFree, which found and fixed a case
// where an earlier version of that test claimed to test a concurrent
// Close but actually ran it strictly after everything else - this
// test is written to genuinely race from the start, avoiding that
// mistake here). Several goroutines repeatedly call sendBytes while
// one, started at the same time rather than afterward, calls Close -
// once Close wins that race, later sendBytes calls legitimately
// start failing (the file is gone), which is expected and not a
// failure; only a genuine race (caught by -race) or a panic should
// fail this test.
func TestPort_Close_ConcurrentWithSendBytesIsRaceFree(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)
	frame := []byte{0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC, 0xCC}

	var wg sync.WaitGroup
	const senders = 5
	wg.Add(senders)
	for i := 0; i < senders; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := p.sendBytes(frame, nil); err != nil {
					return // expected once Close() has won the race
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := p.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	wg.Wait()
}

// flushInput's error branch (the TCFLSH ioctl failing) had no
// recvBytes' own "genuine read error" branch (as opposed to a plain
// timeout, already covered elsewhere) had no coverage - Read on an
// already-closed *os.File fails immediately with a real, non-EOF
// error, giving a deterministic way to trigger it without needing a
// pty-level fault.
func TestPort_RecvBytes_ReadErrorPropagates(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)
	fake.Slave.Close()

	_, err := p.recvBytes(make([]byte, 16), time.Second)
	if err == nil {
		t.Fatal("recvBytes on a closed fd: want error, got nil")
	}
	if !strings.Contains(err.Error(), "mcu: read:") {
		t.Errorf("recvBytes error = %q, want it wrapped with \"mcu: read:\" context", err.Error())
	}
}

// coverage - same technique as TestPort_SendBytes_WriteErrorPropagates:
// close the underlying fd out from under the Port first.
func TestPort_FlushInput_ErrorOnClosedFd(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)
	fake.Slave.Close()

	err := p.flushInput()
	if err == nil {
		t.Fatal("flushInput on a closed fd: want error, got nil")
	}
	if !strings.Contains(err.Error(), "mcu: flush input:") {
		t.Errorf("flushInput error = %q, want it wrapped with \"mcu: flush input:\" context", err.Error())
	}
}

// flushInput's actual purpose (discarding leftover bytes already
// sitting in the kernel's receive buffer from BEFORE a request is
// sent) is specific to a real tty's receive queue persisting across
// opens, which a fresh pty pair in a test can't reproduce - this
// only confirms the call itself succeeds against a pty and doesn't
// error out, and that it drains bytes written before it runs.
func TestPort_FlushInput_DoesNotErrorAndDrainsPendingBytes(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)

	if _, err := fake.Master.Write([]byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("write pending bytes: %v", err)
	}
	// Give the slave's input queue a moment to actually receive what
	// was just written before flushing it - otherwise this could flush
	// before the bytes have arrived, making the test pass for the
	// wrong reason.
	time.Sleep(50 * time.Millisecond)

	if err := p.flushInput(); err != nil {
		t.Fatalf("flushInput: %v", err)
	}

	if _, err := fake.Master.Write([]byte{0xAA}); err != nil {
		t.Fatalf("write post-flush byte: %v", err)
	}
	got, err := p.recvBytes(make([]byte, 16), time.Second)
	if err != nil {
		t.Fatalf("recvBytes: %v", err)
	}
	if got != 1 {
		t.Fatalf("recvBytes returned %d byte(s), want exactly 1 (the pre-flush bytes should have been discarded)", got)
	}
}

// mcuTrace/traceEvent (diagnostic-only, see their own doc comment)
// are gated behind an env var read once at package-init time, so a
// normal `go test` run never exercises their body - found via
// `go tool cover`. mcuTrace is a plain package var, not a const, so
// a test in this same package can flip it directly for its own
// duration and restore it afterward, without needing a separate
// WD_MCU_TRACE=1 process invocation. Confirms both call sites that
// matter (traceEvent itself, and flushInput's own FIONREAD+traceEvent
// block) run without panicking or erroring when tracing is on - not
// asserting on the actual stderr text, which is debug output, not a
// documented contract.
func TestFlushInput_TraceEnabled_DoesNotPanicOrError(t *testing.T) {
	old := mcuTrace
	mcuTrace = true
	defer func() { mcuTrace = old }()

	traceEvent("test event %d", 42)

	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)
	if err := p.flushInput(); err != nil {
		t.Fatalf("flushInput with tracing enabled: %v", err)
	}
}

// Regression test for the exact real-hardware bug report this
// project got: a real EX2 RDateAndTime reply (13 bytes) was caught
// truncated to 8 bytes by a single Read() call that returned before
// the rest of the reply had arrived on the wire. Writes the reply
// one byte at a time with a real inter-byte gap (1ms - the same
// order of magnitude as the ~0.5-0.7ms gaps this project's own
// strace captures show, not just an instant bulk write that would
// let this pass for the wrong reason) and confirms recvBytes
// assembles the FULL reply instead of returning whatever the first
// Read() happened to catch.
func TestPort_RecvBytes_AssemblesReplyArrivingOverMultipleReads(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)

	want := []byte{0xFA, 0x01, 0x08, 0x01, 0x01, 0x43, 0x39, 0x13, 0x01, 0x02, 0x11, 0x26, 0xFB}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, b := range want {
			if _, err := fake.Master.Write([]byte{b}); err != nil {
				t.Errorf("write byte %#x: %v", b, err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	buf := make([]byte, 64)
	n, err := p.recvBytes(buf, 3*time.Second)
	<-done
	if err != nil {
		t.Fatalf("recvBytes: %v", err)
	}
	if n != len(want) {
		t.Fatalf("recvBytes returned %d byte(s) (% X), want the full %d-byte reply (% X)", n, buf[:n], len(want), want)
	}
	if string(buf[:n]) != string(want) {
		t.Errorf("recvBytes = % X, want % X", buf[:n], want)
	}
}

// The other side of readPollInterval: once a reply genuinely stops
// (not just a slow byte in transit), recvBytes must not wait out the
// full outer timeout before returning what it has - that would make
// every reply as slow as the "nothing ever arrives" case. Writes
// only a prefix, stays silent, and confirms recvBytes returns well
// before the 3-second outer timeout (bounded by one empty,
// readPollInterval-bounded Read() instead).
func TestPort_RecvBytes_ReturnsAfterQuietWindowNotFullTimeout(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)

	prefix := []byte{0xFA, 0x01, 0x08, 0x01, 0x01}
	if _, err := fake.Master.Write(prefix); err != nil {
		t.Fatalf("write prefix: %v", err)
	}

	start := time.Now()
	buf := make([]byte, 64)
	n, err := p.recvBytes(buf, 3*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("recvBytes: %v", err)
	}
	if n != len(prefix) {
		t.Fatalf("recvBytes returned %d byte(s), want exactly the %d-byte prefix (no more was ever sent)", n, len(prefix))
	}
	if elapsed >= 3*time.Second {
		t.Errorf("recvBytes took %v, want well under the 3s outer timeout (bounded by readPollInterval instead)", elapsed)
	}
}

// CONFIRMED BUG regression test: an earlier design (a fresh goroutine
// per Read() attempt, including the trailing "is there more" probes -
// see readPollInterval's own doc comment for the full history) left
// a goroutine behind whenever such a probe's own short timeout
// elapsed with nothing new. That abandoned goroutine and a SECOND
// recvBytes call's own fresh one both ended up blocked on Read()
// concurrently, and the kernel could deliver the second call's own
// reply to the first call's abandoned leftover instead - the second
// call then saw nothing and timed out despite the reply having
// actually arrived. Calls recvBytes twice in a row on the same Port
// (mirroring cmd/wd-control-tool/main.go's buildRTCReadAction
// retrying a decode failure) and confirms the SECOND call gets its
// own reply, not nothing.
func TestPort_RecvBytes_SecondCallOnSamePortSeesItsOwnReply(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)

	first := []byte{0x30, 0x00, 0x00, 0x00, 0x00, 0xFB} // same unrecoverable shape used elsewhere in this project
	if _, err := fake.Master.Write(first); err != nil {
		t.Fatalf("write first reply: %v", err)
	}
	buf1 := make([]byte, 64)
	n1, err := p.recvBytes(buf1, 3*time.Second)
	if err != nil {
		t.Fatalf("first recvBytes: %v", err)
	}
	if n1 != len(first) {
		t.Fatalf("first recvBytes returned %d byte(s), want %d", n1, len(first))
	}

	second := []byte{0xFA, 0x01, 0x08, 0x01, 0x01, 0x43, 0x39, 0x13, 0x01, 0x02, 0x11, 0x26, 0xFB}
	if _, err := fake.Master.Write(second); err != nil {
		t.Fatalf("write second reply: %v", err)
	}
	buf2 := make([]byte, 64)
	n2, err := p.recvBytes(buf2, 3*time.Second)
	if err != nil {
		t.Fatalf("second recvBytes: %v", err)
	}
	if n2 != len(second) {
		t.Fatalf("second recvBytes returned %d byte(s) (% X), want the full %d-byte second reply (% X) - got nothing/wrong-size here is exactly the regression this test guards", n2, buf2[:n2], len(second), second)
	}
	if string(buf2[:n2]) != string(second) {
		t.Errorf("second recvBytes = % X, want % X", buf2[:n2], second)
	}
}

// recvBytes' "buffer full" branch had no coverage - every other test
// writes far fewer bytes than sendAndRecv's own 64-byte buffer.
// Passes a deliberately small buf instead of changing what's written,
// and confirms recvBytes stops exactly at buf's own capacity rather
// than attempting to copy past it (which would panic) or looping
// forever.
func TestPort_RecvBytes_StopsAtBufferCapacity(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)

	if _, err := fake.Master.Write([]byte{0x01, 0x02, 0x03, 0x04}); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 3)
	n, err := p.recvBytes(buf, 3*time.Second)
	if err != nil {
		t.Fatalf("recvBytes: %v", err)
	}
	if n != len(buf) {
		t.Errorf("recvBytes returned %d byte(s), want exactly %d (buf's own capacity)", n, len(buf))
	}
}
