// Package fakemcu stands in for the real MCU on the other end of a
// UART, so pkg/mcu's frame-building and transmit code can be
// exercised end-to-end (real io.Writer, real byte-at-a-time writes,
// real pacing) without real hardware.
//
// It opens a Linux pty pair: Slave is wrapped as a *mcu.Port via
// mcu.OpenFile (bypassing termios configuration, which has nothing
// meaningful to assert on a pty anyway - the point of this harness is
// verifying frame CONTENT and ORDER, not real UART timing); Master is
// what a test reads from (to see exactly what the tool transmitted)
// and can write to, to script a fake MCU reply - now routinely used
// for that (EX2U's temperature reply, EX2's fan RPM reply, EX2's
// RDateAndTime reply - all three decoded via a live capture; see
// pkg/mcu/ex2u_test.go's/ex2_test.go's own *Reply tests), not just a
// someday-maybe as this comment used to say. Most MCU replies remain
// undecoded - see SPEC.md, "unresolved".
package fakemcu

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Fake is one open pty pair.
type Fake struct {
	Master    *os.File
	Slave     *os.File
	SlavePath string // lets a test call mcu.Open(SlavePath, baud) itself, exercising the real Open path (termios setup included) instead of mcu.OpenFile(Slave)
}

// setRaw strips the line discipline's cooked-mode processing
// (canonical input, output post-processing/CR-LF translation, echo,
// signal generation) so bytes written to f arrive at the other end
// of the pty unmodified - see the comment at setRaw's call site for
// why this matters here.
func setRaw(f *os.File) error {
	got, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		return err
	}
	raw := *got
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	// VMIN=0/VTIME=1 (100ms), matching mcu.Open's own real-device
	// config exactly (see pkg/mcu/serial.go's readPollInterval) - not
	// VMIN=1/VTIME=0. A fake pty under that older config was how
	// mcu.(*Port).Close's inability to reliably unblock a goroutine
	// already parked in Read() on a REAL device's termios was first
	// reproduced at all: this field must track Open's own VMIN/VTIME
	// choice exactly, or this harness would exercise different
	// blocking behavior than what real hardware actually does.
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 1
	return unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, &raw)
}

// New opens a fresh pty pair and registers cleanup on t.
func New(t *testing.T) *Fake {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("fakemcu: open /dev/ptmx: %v", err)
	}

	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatalf("fakemcu: unlock pty: %v", err)
	}

	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatalf("fakemcu: get pty number: %v", err)
	}

	slavePath := fmt.Sprintf("/dev/pts/%d", n)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatalf("fakemcu: open %s: %v", slavePath, err)
	}

	// A freshly opened pty slave defaults to cooked mode (ICANON,
	// OPOST|ONLCR, ...), unlike a real serial device after
	// mcu.Open's termios setup. Without this, a byte in our frame
	// that happens to equal '\n' (0x0A) - which SysName's own LEN
	// byte does whenever the text is exactly 10 bytes long, as this
	// package's own tests found the hard way - gets expanded to
	// "\r\n" by the line discipline before the master ever sees it,
	// silently corrupting the "wire" bytes a test reads. Put the
	// slave in raw mode so what the master reads is exactly what
	// mcu.Port wrote, matching what a real UART (always opened raw
	// by mcu.Open) would deliver.
	if err := setRaw(slave); err != nil {
		slave.Close()
		master.Close()
		t.Fatalf("fakemcu: set slave raw mode: %v", err)
	}

	f := &Fake{Master: master, Slave: slave, SlavePath: slavePath}
	t.Cleanup(func() {
		slave.Close()
		master.Close()
	})
	return f
}

// ReadFrame reads exactly n bytes from Master (what the tool under
// test sent), failing the test if they don't arrive within timeout.
// Use this rather than a bare Read - a test asserting on a short
// frame must not silently pass on a partial write.
//
// This does NOT rely on (*os.File).SetReadDeadline, despite that
// being the obvious approach: on a /dev/ptmx-derived master fd in
// this environment, a deadline is accepted without error but simply
// does not unblock a pending Read - confirmed by watching a bare
// Read block for 5+ seconds past a 500ms deadline in isolation, not
// by assumption (an earlier version of a test in this package relied
// on it and hung a `go test` run past its own -timeout). Instead, the
// read happens in its own goroutine and the timeout is enforced with
// select/time.After, which works regardless of whether the fd itself
// honors deadlines. If the timeout fires, that goroutine is left
// blocked on Read permanently (a deliberate, harmless leak - the test
// binary process exits once the test run finishes either way), which
// is the standard tradeoff for this pattern.
func (f *Fake) ReadFrame(t *testing.T, n int, timeout time.Duration) []byte {
	t.Helper()
	buf, err := readExactly(f.Master, n, timeout)
	if err != nil {
		t.Fatalf("fakemcu: %v", err)
	}
	return buf
}

// readExactly is ReadFrame's actual logic, factored out (and taking
// a plain io.Reader, not *Fake) so it's directly unit-testable - in
// particular so the "times out close to the requested duration,
// rather than hanging" behavior that made this package switch away
// from SetReadDeadline can be pinned with a real assertion on
// elapsed time, not just "the overall test suite didn't hang".
func readExactly(r io.Reader, n int, timeout time.Duration) ([]byte, error) {
	type result struct {
		buf []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, n)
		got := 0
		for got < n {
			k, err := r.Read(buf[got:])
			if err != nil {
				ch <- result{nil, err}
				return
			}
			got += k
		}
		ch <- result{buf, nil}
	}()

	select {
	case res := <-ch:
		return res.buf, res.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out after %v waiting for %d bytes", timeout, n)
	}
}
