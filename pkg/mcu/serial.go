package mcu

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// baudToTermios maps the handful of baud rates this board family
// actually uses to their termios Bxxxxx constants. Confirmed by RE:
// EX2U uart1 = 19200; EX2 /dev/ttyS1 = 115200; EX4 /dev/ttyS1 = 19200.
// See SPEC.md, "UART line settings".
var baudToTermios = map[int]uint32{
	19200:  unix.B19200,
	115200: unix.B115200,
}

// Port is a raw, 8N1, no-flow-control serial line to an MCU.
type Port struct {
	f *os.File
}

// Open configures path (e.g. /dev/ttyS1, or uart1's tty node on EX2U)
// for raw 8N1 at baud and returns it ready for Send/Recv. No vendor
// library (librs232.so) is involved - this is a direct termios ioctl,
// equivalent to what RS232_Open/RS232_SetParam did internally.
func Open(path string, baud int) (*Port, error) {
	b, ok := baudToTermios[baud]
	if !ok {
		return nil, fmt.Errorf("mcu: unsupported baud rate %d", baud)
	}

	f, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY|unix.O_SYNC, 0)
	if err != nil {
		return nil, fmt.Errorf("mcu: open %s: %w", path, err)
	}

	t := unix.Termios{
		Iflag:  0,
		Oflag:  0,
		Cflag:  unix.CS8 | unix.CREAD | unix.CLOCAL,
		Lflag:  0,
		Ispeed: b,
		Ospeed: b,
	}
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, &t); err != nil {
		f.Close()
		return nil, fmt.Errorf("mcu: configure %s: %w", path, err)
	}

	return &Port{f: f}, nil
}

// OpenFile wraps an already-open file (a test PTY, typically) as a
// Port without touching termios - used by test/fakemcu so tests can
// exercise the exact same frame-building/pacing code against a fake
// MCU on the other end of a PTY pair.
func OpenFile(f *os.File) *Port { return &Port{f: f} }

func (p *Port) Close() error { return p.f.Close() }

// sendBytes writes b one byte at a time with the given inter-byte
// pacing, matching the vendor daemons' own transmit loop (see
// SPEC.md: EX2U uses ~100ms mdelay-style pacing; EX2/EX4's
// up_send_daemon used usleep(10us), which is negligible next to the
// UART's own bit time - but it is reproduced here for parity).
func (p *Port) sendBytes(b []byte, pace func()) error {
	for _, c := range b {
		if _, err := p.f.Write([]byte{c}); err != nil {
			return fmt.Errorf("mcu: write: %w", err)
		}
		if pace != nil {
			pace()
		}
	}
	return nil
}
