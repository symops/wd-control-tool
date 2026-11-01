package mcu

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/test/fakemcu"
	"golang.org/x/sys/unix"
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

// The real regression test for the actual root cause of the EX2U
// hardware failure: Open set Ispeed/Ospeed but never OR'd the baud
// constant into Cflag itself. TCSETS (unlike TCSETS2) uses the
// kernel's plain struct termios, which has no ispeed/ospeed fields at
// all - the baud rate lives entirely in c_cflag's CBAUD bits - so
// that bug silently left every port at B0 (0 baud) regardless of the
// baud argument. No PTY-based test can catch this: a pty has no bit
// rate to get wrong, so every PTY test in this package passed both
// before and after the bug was introduced and fixed. This test reads
// the configured termios back with TCGETS and checks Cflag's CBAUD
// bits directly - the same field a live strace of this tool on real
// EX2U hardware showed as "B0|CS8|CREAD|CLOCAL" instead of the
// vendor mcu_ctl's "B19200|CS8|CREAD|CLOCAL".
func TestOpen_SetsRealBaudRateInCflag(t *testing.T) {
	fake := fakemcu.New(t)

	cases := []struct {
		baud int
		want uint32
	}{
		{19200, unix.B19200},
		{115200, unix.B115200},
	}
	for _, c := range cases {
		p, err := Open(fake.SlavePath, c.baud)
		if err != nil {
			t.Fatalf("Open(..., %d): %v", c.baud, err)
		}

		got, err := unix.IoctlGetTermios(int(p.f.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatalf("baud %d: IoctlGetTermios: %v", c.baud, err)
		}

		if got.Cflag&unix.CBAUD != c.want {
			t.Errorf("baud %d: Cflag&CBAUD = %#x, want %#x (Cflag = %#x) - baud rate not actually set on the wire, only Ispeed/Ospeed (which TCSETS ignores)", c.baud, got.Cflag&unix.CBAUD, c.want, got.Cflag)
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

// Open's termios-configure error branch (IoctlSetTermios failing)
// had zero coverage - every other test opens a real pty, where it
// always succeeds. Closed by pointing Open at a regular file instead
// of a tty: os.OpenFile succeeds (it's a real file), but
// IoctlSetTermios on a non-tty fd fails with ENOTTY ("inappropriate
// ioctl for device") - confirmed by hand first with a throwaway
// probe program before writing this as a permanent test.
func TestOpen_NonTTYPath_ConfigureFails(t *testing.T) {
	f, err := os.CreateTemp("", "wd-control-tool-mcu-test-notty-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	_, err = Open(f.Name(), 19200)
	if err == nil {
		t.Fatal("Open on a regular file (not a tty): want a termios-configure error, got nil")
	}
	var errno unix.Errno
	if !errors.As(err, &errno) || errno != unix.ENOTTY {
		t.Errorf("Open error = %v, want it to wrap ENOTTY (errors.As)", err)
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
