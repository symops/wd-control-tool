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
