package mcu

import (
	"bytes"
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
