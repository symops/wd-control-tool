package mcu

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

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

// findFrame scans buf for the first frameLen-byte window starting
// with 0xFA, ending with 0xFB, with header matching starting at
// offset 1 (so len(header)==2 checks cat/subcmd at offsets 1 and 2;
// EX2DecodeDateAndTime's 13-byte frame also checks B6 at offset 3) -
// skipping over anything else in buf before or after it.
//
// Needed because a specific request's reply isn't reliably at
// buf[0:frameLen]: real EX2 hardware was found to interleave an
// unsolicited `FA 30 00 00 00 00 FB` frame with FanStatusGet's own
// reply (and, separately, with RDateAndTime's), independent of
// request/reply timing (sometimes before it, sometimes after,
// sometimes a leftover one from a previous invocation glued to the
// front of the current one - see (*Port).flushInput, which reduces
// but may not eliminate every case). EX2DecodeFanRPM,
// EX2UDecodeTempFahrenheit and EX2DecodeDateAndTime all call this
// instead of assuming an offset.
func findFrame(buf []byte, header []byte, frameLen int) ([]byte, error) {
	for i := 0; i+frameLen <= len(buf); i++ {
		if buf[i] != 0xFA || buf[i+frameLen-1] != 0xFB {
			continue
		}
		match := true
		for j, h := range header {
			if buf[i+1+j] != h {
				match = false
				break
			}
		}
		if match {
			return buf[i : i+frameLen], nil
		}
	}
	return nil, fmt.Errorf("mcu: no FA % X .. FB (%d-byte) frame found in reply: % X", header, frameLen, buf)
}

// flushInput discards any bytes already waiting to be read on the
// port, without reading them, via TCFLSH(TCIFLUSH) - called right
// before sending a request that expects a reply.
//
// Hardware-confirmed to matter: real EX2 hardware was found to leave
// leftover bytes in the kernel's receive buffer between separate
// invocations of this tool (each one opens a fresh fd, but the
// kernel's own input queue for the device persists across opens) -
// confirmed by a real capture where a reply showed up duplicated
// (the previous invocation's full reply, never read, stuck to the
// front of the new one) and another where it showed up truncated
// with no leading FA at all (a previous invocation's tail end,
// missing its own head because something upstream had already
// consumed it). TCIFLUSH (0) is the standard POSIX value - not
// separately exported by golang.org/x/sys/unix for linux the way
// TCIOFLUSH/TCOFLUSH sometimes are on other OSes, so it's passed as
// a literal here.
func (p *Port) flushInput() error {
	const tcIFlush = 0
	if err := unix.IoctlSetInt(int(p.f.Fd()), unix.TCFLSH, tcIFlush); err != nil {
		return fmt.Errorf("mcu: flush input: %w", err)
	}
	return nil
}

// sendAndRecv flushes stale input, calls send (a port's own request-
// frame sender, e.g. EX2.RequestFanStatus or EX2U.TempRaw), then
// reads whatever bytes arrive within timeout - the exact sequence
// EX2.FanStatusReply and EX2U.TempReply both need, factored out once
// their bodies turned out byte-for-byte identical apart from which
// send function to call.
func (p *Port) sendAndRecv(send func() error, timeout time.Duration) ([]byte, error) {
	if err := p.flushInput(); err != nil {
		return nil, err
	}
	if err := send(); err != nil {
		return nil, err
	}
	buf := make([]byte, 64)
	n, err := p.recvBytes(buf, timeout)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// recvBytes reads whatever bytes arrive on the port within timeout.
// Zero bytes with a nil error is a normal "nothing arrived in time"
// outcome, not a failure - independent of whether this project has a
// decoder for the reply at all: EX2U.TempReply's reply IS decoded
// (EX2UDecodeTempFahrenheit/Celsius), but every real end-to-end run
// of this tool's own `-t` command has still seen nothing come back
// within the timeout (see buildTempAction's own CLI-level test for
// that), same as it would for a genuinely undecoded reply. A non-nil
// error means the read itself failed.
//
// Open configures VMIN=1/VTIME=0, so the underlying Read blocks
// until at least one byte is available - there is no kernel-level
// per-read timeout to lean on. (*os.File).SetReadDeadline was ruled
// out for this exact reason on a pty master in this project before
// (see test/fakemcu's readExactly) and a real tty fd gives no
// stronger guarantee, so the same goroutine+select pattern is used
// here: the Read runs in its own goroutine and the timeout is
// enforced with select/time.After regardless of whether the fd
// honors a deadline.
//
// A timeout leaves that goroutine blocked on Read past this call's
// return - harmless for this tool's one-action-per-process CLI usage
// (the process exits right after run() returns, taking the goroutine
// with it), but calling recvBytes again on the same Port before an
// earlier timed-out Read has actually returned risks two concurrent
// Reads racing for the same incoming bytes.
func (p *Port) recvBytes(buf []byte, timeout time.Duration) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := p.f.Read(buf)
		ch <- result{n, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && r.err != io.EOF {
			return r.n, fmt.Errorf("mcu: read: %w", r.err)
		}
		return r.n, nil
	case <-time.After(timeout):
		return 0, nil
	}
}
