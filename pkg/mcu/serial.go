package mcu

import (
	"fmt"
	"os"
	"sync"

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

// Port is a raw, 8N1, no-flow-control serial line to an MCU. Safe
// for concurrent use: sendBytes and Close both take mu, so two
// goroutines sending frames on the same Port serialize rather than
// interleave their bytes mid-frame on the wire - a real hazard this
// type would otherwise have (found via the same review that caught
// regio.Mem's Close not taking its own mutex), even though nothing in
// this project's own CLI currently shares one Port across goroutines
// (run() dispatches actions sequentially).
type Port struct {
	mu sync.Mutex
	f  *os.File
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

	// b must be OR'd into Cflag itself, not just Ispeed/Ospeed: TCSETS
	// (unlike TCSETS2) uses the kernel's plain struct termios, whose
	// C layout has no ispeed/ospeed fields at all - the baud rate is
	// entirely encoded in c_cflag's CBAUD bits. unix.Termios's
	// Ispeed/Ospeed fields exist in the Go struct but are silently
	// ignored by a TCSETS ioctl, so setting only those (the original
	// bug here) leaves the real baud rate at B0 (hang-up/0 baud) -
	// confirmed via a live strace comparing this tool's own TCSETS
	// call (c_cflag=B0|CS8|CREAD|CLOCAL) against the vendor mcu_ctl's
	// (c_cflag=B19200|CS8|CREAD|CLOCAL) on real EX2U hardware. This,
	// not the inter-byte pacing fixed earlier, was the actual cause of
	// "frame sent with no I/O error, MCU never responds": the bytes
	// went out at the wrong bit rate, so the MCU's UART never saw a
	// valid frame, let alone a complete one.
	t := unix.Termios{
		Iflag:  0,
		Oflag:  0,
		Cflag:  b | unix.CS8 | unix.CREAD | unix.CLOCAL,
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

func (p *Port) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.f.Close()
}

// sendBytes writes b one byte at a time with the given inter-byte
// pacing (nil for none), matching the vendor tools' own transmit
// loop (see SPEC.md: EX2U's mcu_ctl has no deliberate delay at all -
// corrected after a real-hardware failure traced back to a wrong
// assumption that it slept ~100ms between bytes; EX2/EX4's
// up_send_daemon used usleep(10us), negligible next to the UART's
// own bit time but reproduced here for parity).
// Holds mu for the whole frame (including the inter-byte sleeps), so
// a concurrent sender's frame can only start after this one's FB (or
// equivalent end byte) has gone out, never interleaved with it.
func (p *Port) sendBytes(b []byte, pace func()) error {
	p.mu.Lock()
	defer p.mu.Unlock()
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
