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
