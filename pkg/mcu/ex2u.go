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

// ex2uFrame is the pure (no I/O) frame builder every EX2U command
// goes through - factored out so the CLI's -n/--dry-run and -D/--debug
// can compute and display the exact bytes a command would send
// without opening a serial port at all.
func ex2uFrame(cat, subcmd, param byte) []byte {
	return []byte{ex2uStart, cat, subcmd, param, 0x00, 0x00, ex2uEnd}
}

func (m *EX2U) send(cat, subcmd, param byte) error {
	return sendEX2U(m.p, ex2uFrame(cat, subcmd, param))
}

func sendEX2U(p *Port, frame []byte) error {
	return p.sendBytes(frame, func() { time.Sleep(ex2uInterByteDelay) })
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

// EX2ULEDStates lists the CLI vocabulary EX2ULEDFrame/the `-l` command
// accept, in a stable order - used both for parsing and for printing
// a useful error message.
var EX2ULEDStates = []string{"on", "off", "blinking", "breathing"}

// EX2ULEDFrame builds the frame for EX2U's status LED, from the same
// state vocabulary the CLI's `-l/--led -c STATE` accepts.
func EX2ULEDFrame(state string) ([]byte, error) {
	switch state {
	case "on":
		return ex2uFrame(0x03, 0x06, 0x01), nil
	case "off":
		return ex2uFrame(0x03, 0x06, 0x00), nil
	case "blinking", "blink":
		return ex2uFrame(0x03, 0x06, 0x02), nil
	case "breathing", "breathe":
		return ex2uFrame(0x03, 0x06, 0x04), nil
	default:
		return nil, errUnknownCommand(state)
	}
}

// ex2uFanPercentByte maps the 5 confirmed duty-cycle steps to their
// wire byte - shared by EX2UFanPercentFrame and FanPercent.
func ex2uFanPercentByte(pct int) (byte, error) {
	switch pct {
	case 0:
		return 0x00, nil
	case 25:
		return 0x40, nil
	case 50:
		return 0x80, nil
	case 75:
		return 0xC0, nil
	case 100:
		return 0xFF, nil
	default:
		return 0, errUnsupportedFanLevel(pct)
	}
}

// EX2UFanPercentFrame builds the frame for EX2U's fan, accepting only
// the 5 duty-cycle steps the real MCU_CTL exposes (0/25/50/75/100);
// anything else is a usage error rather than guessing an interpolated
// byte the MCU was never confirmed to accept.
func EX2UFanPercentFrame(pct int) ([]byte, error) {
	b, err := ex2uFanPercentByte(pct)
	if err != nil {
		return nil, err
	}
	return ex2uFrame(0x02, 0x00, b), nil
}

// FanPercent sends EX2UFanPercentFrame's bytes.
func (m *EX2U) FanPercent(pct int) error {
	b, err := ex2uFanPercentByte(pct)
	if err != nil {
		return err
	}
	return m.send(0x02, 0x00, b)
}
