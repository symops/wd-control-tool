package mcu

import "time"

// EX2U's Welltrend 6703F-OG240WT MCU protocol, on uart1, 19200 8N1.
// Hardware-confirmed (captured via strace on real hardware, and the
// shutdown command end-to-end tested against the kernel driver that
// now also speaks it natively - see SPEC.md, "EX2U protocol").
//
// Frame: 7 bytes, FA <cat> <subcmd> <param> 00 00 FB, one byte at a
// time with ~100ms inter-byte pacing. No checksum.

const (
	ex2uStart = 0xFA
	ex2uEnd   = 0xFB
	// The real hardware's own defensive margin, reproduced here rather
	// than risk finding out it was load-bearing. See SPEC.md.
	ex2uInterByteDelay = 100 * time.Millisecond
)

// EX2U is a Port bound to the Welltrend MCU's command set.
type EX2U struct{ p *Port }

// NewEX2U wraps an already-opened Port (19200 8N1) as an EX2U MCU
// client.
func NewEX2U(p *Port) *EX2U { return &EX2U{p: p} }

func (m *EX2U) send(cat, subcmd, param byte) error {
	frame := []byte{ex2uStart, cat, subcmd, param, 0x00, 0x00, ex2uEnd}
	return m.p.sendBytes(frame, func() { time.Sleep(ex2uInterByteDelay) })
}

// LED state for EX2U's single blue-only "status" LED (not GPIO, not
// any of the hd0/hd1 bay LEDs - those are plain kernel sysfs, out of
// scope for this tool; see SPEC.md).
func (m *EX2U) LEDOn() error      { return m.send(0x03, 0x06, 0x01) }
func (m *EX2U) LEDOff() error     { return m.send(0x03, 0x06, 0x00) }
func (m *EX2U) LEDBlink() error   { return m.send(0x03, 0x06, 0x02) }
func (m *EX2U) LEDBreathe() error { return m.send(0x03, 0x06, 0x04) }

func (m *EX2U) SysReady() error  { return m.send(0x03, 0x01, 0x00) }
func (m *EX2U) SysSilent() error { return m.send(0x03, 0x03, 0x00) }

// SysShutdown is a genuine, confirmed hard power cut on real
// hardware - not cosmetic. See SPEC.md before wiring this up to
// anything that isn't an actual, deliberate shutdown path.
func (m *EX2U) SysShutdown() error { return m.send(0x03, 0x03, 0x01) }

// TempRaw sends the one wire command both tmp_get_f and tmp_get_c use;
// unit conversion (if any) is the caller's job, not the protocol's -
// this tool has never captured a response frame for this command, so
// for now it only sends the request (see SPEC.md "unresolved").
func (m *EX2U) TempRaw() error { return m.send(0x03, 0x08, 0x00) }

// FanPercent accepts only the 5 duty-cycle steps the real MCU_CTL
// exposes (0/25/50/75/100); anything else is a usage error rather
// than guessing an interpolated byte the MCU was never confirmed to
// accept.
func (m *EX2U) FanPercent(pct int) error {
	var b byte
	switch pct {
	case 0:
		b = 0x00
	case 25:
		b = 0x40
	case 50:
		b = 0x80
	case 75:
		b = 0xC0
	case 100:
		b = 0xFF
	default:
		return errUnsupportedFanLevel(pct)
	}
	return m.send(0x02, 0x00, b)
}
