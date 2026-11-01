package mcu

import "time"

// EX2U's Welltrend 6703F-OG240WT MCU protocol, on uart1, 19200 8N1.
// Hardware-confirmed (captured via strace on real hardware, and the
// shutdown command end-to-end tested against the kernel driver that
// now also speaks it natively - see SPEC.md, "EX2U protocol").
//
// Frame: 7 bytes, FA <cat> <subcmd> <param> 00 00 FB, one byte at a
// time with NO deliberate inter-byte delay. No checksum.
//
// CORRECTED after a real-hardware failure report: this file
// previously slept 100ms between bytes, based on a (wrong, never
// actually strace-confirmed) assumption that this was "the real
// hardware's own margin". A live `strace -tt` capture of the real,
// working `mcu_ctl fan_set_50` on actual EX2U hardware (the same
// house rule this project always uses - never guess, capture) showed
// consecutive single-byte write() calls roughly 0.5ms apart - pure
// syscall/O_SYNC overhead, no explicit sleep at all. Sending with a
// 100ms gap instead (200x slower) let every write() succeed at the
// OS level (no error ever surfaced) while the Welltrend MCU's own
// frame-reception state machine most likely timed out between bytes
// and never saw a complete, valid frame - explaining exactly the
// symptom reported: no error, but the hardware never responded. See
// SPEC.md's "EX2U protocol" section for the exact captured timings.

const (
	ex2uStart = 0xFA
	ex2uEnd   = 0xFB
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
	return p.sendBytes(frame, nil) // no deliberate inter-byte delay - see the file's own doc comment
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

// EX2UTempRequestFrame builds the fixed request frame both tmp_get_f
// and tmp_get_c send (unit conversion, if any, is client-side - see
// SPEC.md's command table) - the pure builder -n/--dry-run and
// -D/--debug use, matching every other EX2U command.
func EX2UTempRequestFrame() []byte { return ex2uFrame(0x03, 0x08, 0x00) }

// TempRaw sends the one wire command both tmp_get_f and tmp_get_c use;
// unit conversion (if any) is the caller's job, not the protocol's -
// this tool has never captured a response frame for this command, so
// for now it only sends the request (see SPEC.md "unresolved").
func (m *EX2U) TempRaw() error { return m.send(0x03, 0x08, 0x00) }

// TempReply sends the tmp_get request and returns whatever raw bytes
// the MCU sends back within timeout - possibly none. Deliberately
// returns the UNDECODED bytes rather than a parsed value: see
// EX2UDecodeTempFahrenheit for the decode, layered on top of this
// rather than built into it, so a caller can always see exactly what
// came back even when decoding fails or a future reply shape differs
// from the one captured so far.
func (m *EX2U) TempReply(timeout time.Duration) ([]byte, error) {
	if err := m.p.flushInput(); err != nil {
		return nil, err
	}
	if err := m.TempRaw(); err != nil {
		return nil, err
	}
	buf := make([]byte, 64)
	n, err := m.p.recvBytes(buf, timeout)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// EX2UDecodeTempFahrenheit decodes a tmp_get reply (as returned by
// TempReply) into a whole-degree Fahrenheit value.
//
// Hardware-confirmed, but from a single live capture: this tool's
// own `-t` run alongside a simultaneous `mcu_ctl tmp_get_c` on the
// same real EX2U hardware reported "Temperature: 30 C", and the
// captured reply was `FA 03 08 00 00 56 FB FA` - the same fixed
// 7-byte shape every other EX2U frame uses (FA <cat> <subcmd>
// <param> 00 00 FB), except byte 5 - always 0x00 in a REQUEST - was
// 0x56 (86 decimal). 30°C is exactly 86°F (30*9/5+32 = 86.0, not a
// rounding coincidence), so byte 5 is the temperature in whole-degree
// Fahrenheit - matching tmp_get_f/tmp_get_c sharing one wire command
// (see SPEC.md's command table). The capture's trailing 8th byte
// (0xFA) was not explained at the time; EX2's MCU was later found to
// send an unsolicited, unrelated frame independent of request/reply
// timing (see findFrame's doc comment) - plausibly the same thing
// happening here, the start of one such frame on EX2U too, though
// that's not independently confirmed for this board.
//
// Scans reply for the matching frame via findFrame rather than
// assuming it starts at offset 0, for the same reason.
func EX2UDecodeTempFahrenheit(reply []byte) (int, error) {
	frame, err := findFrame(reply, 0x03, 0x08)
	if err != nil {
		return 0, err
	}
	return int(frame[5]), nil
}

// EX2UDecodeTempCelsius is EX2UDecodeTempFahrenheit converted to
// Celsius via plain truncating integer division ((F-32)*5/9), NOT
// rounded to the nearest degree - confirmed by a second live
// capture: 84°F (0x54) alongside a simultaneous `mcu_ctl tmp_get_c`
// reporting "Temperature: 28 C". (84-32)*5/9 = 28.89 rounds to 29
// but truncates to 28 - the vendor's own tmp_get_c truncates
// (classic C integer-division behavior), and an earlier version of
// this function that rounded instead matched the FIRST capture
// (86°F -> exactly 30, where rounding and truncation agree) purely
// by coincidence, then disagreed with the vendor on this second one.
func EX2UDecodeTempCelsius(reply []byte) (int, error) {
	f, err := EX2UDecodeTempFahrenheit(reply)
	if err != nil {
		return 0, err
	}
	return (f - 32) * 5 / 9, nil
}

// EX2ULEDStates lists the CLI vocabulary EX2ULEDFrame/the `-l` command
// accept, in a stable order - used both for parsing and for printing
// a useful error message.
var EX2ULEDStates = []string{"on", "off", "blinking", "breathing"}

// EX2ULEDFrame builds the frame for EX2U's status LED, from the same
// state vocabulary the CLI's `-l/--led STATE` accepts.
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
		return nil, errUnknownLEDState(state)
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
