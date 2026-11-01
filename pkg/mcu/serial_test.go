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

	const rounds = 25
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
