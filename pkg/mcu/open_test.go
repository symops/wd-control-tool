package mcu

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/test/fakemcu"
)

// Open itself (termios setup included) had zero test coverage before
// this - every other test used OpenFile to bypass it entirely. Open
// a second fd to the same pty slave via its path, so Open's real
// code path (baud validation, os.OpenFile, IoctlSetTermios) runs
// unmodified.

func TestOpen_ValidBaud_SendsOverWire(t *testing.T) {
	fake := fakemcu.New(t)

	for _, baud := range []int{19200, 115200} {
		p, err := Open(fake.SlavePath, baud)
		if err != nil {
			t.Fatalf("Open(%q, %d): %v", fake.SlavePath, baud, err)
		}

		done := make(chan error, 1)
		go func() { done <- NewEX2U(p).SysReady() }()

		got := fake.ReadFrame(t, 7, 3*time.Second)
		if err := <-done; err != nil {
			t.Fatalf("baud %d: SysReady: %v", baud, err)
		}
		want := []byte{0xFA, 0x03, 0x01, 0x00, 0x00, 0x00, 0xFB}
		if !bytes.Equal(got, want) {
			t.Errorf("baud %d: wire bytes = % X, want % X", baud, got, want)
		}
		p.Close()
	}
}

func TestOpen_UnsupportedBaud(t *testing.T) {
	fake := fakemcu.New(t)
	for _, baud := range []int{0, 9600, 38400, 57600, -1} {
		if _, err := Open(fake.SlavePath, baud); err == nil {
			t.Errorf("Open(..., %d): want error, got nil", baud)
		}
	}
}

func TestOpen_NonexistentPath(t *testing.T) {
	if _, err := Open("/nonexistent/wd-control-tool-mcu-test-path", 19200); err == nil {
		t.Error("Open on a nonexistent path: want error, got nil")
	}
}

// Open wraps os.OpenFile's error with %w ("mcu: open %s: %w"), so a
// caller should be able to tell "the device doesn't exist" (e.g. a
// wrong --serial path, exactly what happened on real EX2U hardware
// where the real uart1 node turned out not to be /dev/ttyS1 - see
// SPEC.md's Unresolved #7) apart from other failure modes via
// errors.Is/As, not just by matching the error string. Confirm the
// %w wrapping actually preserves that, rather than just assuming
// fmt.Errorf's %w verb works here the way it's supposed to.
func TestOpen_NonexistentPath_ErrorIsOsErrNotExist(t *testing.T) {
	_, err := Open("/nonexistent/wd-control-tool-mcu-test-path", 19200)
	if err == nil {
		t.Fatal("Open on a nonexistent path: want error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open error = %v, want errors.Is(err, os.ErrNotExist) to hold", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("Open error = %v, want errors.As(err, &os.PathError{}) to hold (the real failing path should be recoverable)", err)
	}
}

func TestPort_Close(t *testing.T) {
	fake := fakemcu.New(t)
	p, err := Open(fake.SlavePath, 19200)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// sendBytes's own pace parameter is optional (nil skips pacing), but
// every production call site (sendEX2U, sendLegacy) always passes a
// real closure - nothing exercises the nil case. Call it directly
// (same package) to cover it.
func TestPort_SendBytes_NilPace(t *testing.T) {
	fake := fakemcu.New(t)
	p := OpenFile(fake.Slave)

	want := []byte{0x01, 0x02, 0x03}
	done := make(chan error, 1)
	go func() { done <- p.sendBytes(want, nil) }()

	got := fake.ReadFrame(t, len(want), time.Second)
	if err := <-done; err != nil {
		t.Fatalf("sendBytes: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}
