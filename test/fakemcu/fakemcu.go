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
// and can write to (to script a fake MCU reply, for the day this
// project decodes the reply framing - see SPEC.md, "unresolved").
package fakemcu

import (
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Fake is one open pty pair.
type Fake struct {
	Master *os.File
	Slave  *os.File
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
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
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

	f := &Fake{Master: master, Slave: slave}
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
func (f *Fake) ReadFrame(t *testing.T, n int, timeout time.Duration) []byte {
	t.Helper()

	if err := f.Master.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("fakemcu: set read deadline: %v", err)
	}

	buf := make([]byte, n)
	got := 0
	for got < n {
		k, err := f.Master.Read(buf[got:])
		if err != nil {
			t.Fatalf("fakemcu: read %d/%d bytes: %v", got, n, err)
		}
		got += k
	}
	return buf
}
