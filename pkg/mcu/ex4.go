package mcu

import "fmt"

// EX4's legacy MCU, /dev/ttyS1, 19200 8N1.
//
// Table rows below are exact byte-for-byte extractions from the real
// up_send_ctl binary's command table (82 rows total; only the rows
// this tool currently wires up are listed here - see SPEC.md for the
// full dump). Do not hand-edit these constants without re-extracting
// from the binary: they are not derived from any documented spec.
var (
	ex4FanControl     = legacyCmd{"FanControl", 0x02, 0x00, 0x00, 0x00, 0x00}
	ex4RDateAndTime   = legacyCmd{"RDateAndTime", 0x01, 0x00, 0x03, 0x01, 0x00}
	ex4WDateAndTime   = legacyCmd{"WDateAndTime", 0x01, 0x00, 0x04, 0x07, 0x00}
	ex4SysName        = legacyCmd{"SysName", 0x04, 0x01, 0x00, 0x00, 0x00}
	ex4DeviceReady    = legacyCmd{"DeviceReady", 0x03, 0x01, 0x00, 0x00, 0x00}
	ex4DeviceShutdown = legacyCmd{"DeviceShutdown", 0x03, 0x03, 0x01, 0x01, 0x00}
	ex4MCUReset       = legacyCmd{"MCUReset", 0x03, 0x03, 0x02, 0x01, 0x00}
)

// EX4 is a legacy-protocol client bound to EX4's command table.
type EX4 struct{ p *Port }

func NewEX4(p *Port) *EX4 { return &EX4{p: p} }

// EX4FanLevelFrame builds the frame for FanLevel's level argument,
// without sending it - the pure half used by the CLI's -n/--dry-run
// and -D/--debug (and by FanLevel itself). Levels: 0 Stop, 1 Low,
// 2 Medium, 3 High, 4 Highest (fan_control -f N's own documented
// range; confirmed as a straight passthrough, not remapped).
func EX4FanLevelFrame(level int) ([]byte, error) {
	if level < 0 || level > 4 {
		return nil, errUnsupportedFanLevel(level)
	}
	return buildValueFrame(ex4FanControl, byte(level)), nil
}

// FanLevel sets the fan via the MCU - EX4 has no direct-register fan
// path at all (unlike EX2, see pkg/fan/ex2.go).
func (m *EX4) FanLevel(level int) error {
	f, err := EX4FanLevelFrame(level)
	if err != nil {
		return err
	}
	return sendLegacy(m.p, f)
}

func (m *EX4) DeviceReady() error {
	return sendLegacy(m.p, buildFixedFrame(ex4DeviceReady))
}

// RequestDateAndTime sends the read request; the MCU's reply framing
// was not decoded (see SPEC.md, "unresolved") so this tool cannot yet
// parse the response itself - included for completeness/symmetry with
// WDateAndTime, not yet called from the fan/rtc/led command layer.
func (m *EX4) RequestDateAndTime() error {
	return sendLegacy(m.p, buildFixedFrame(ex4RDateAndTime))
}

// validateDateTimeFields rejects out-of-range WDateAndTime fields
// before they ever reach toBCD. This isn't just "garbage in, garbage
// out": toBCD's ((v/10)<<4)|(v%10) formula, fed a negative value,
// can produce a byte that collides with the frame's own FA/FB
// markers (confirmed: toBCD(-5)=0xFB, toBCD(-6)=0xFA, toBCD(-15)=
// 0xFB, toBCD(-16)=0xFA) - that would corrupt the frame itself, not
// just send the wrong date. Every real caller (rtc.EX4Writer.Set,
// via time.Time's own fields) only ever passes values already in
// range, so this is defense for EX4DateTimeFrame/SetDateAndTime's
// other callers (both exported), not a path exercised day to day.
func validateDateTimeFields(year, mon, dom, dow, hour, min, sec int) error {
	switch {
	case year < 0:
		return fmt.Errorf("mcu: year must be >= 0, got %d", year)
	case mon < 1 || mon > 12:
		return fmt.Errorf("mcu: month must be 1-12, got %d", mon)
	case dom < 1 || dom > 31:
		return fmt.Errorf("mcu: day of month must be 1-31, got %d", dom)
	case dow < 0 || dow > 6:
		return fmt.Errorf("mcu: day of week must be 0-6, got %d", dow)
	case hour < 0 || hour > 23:
		return fmt.Errorf("mcu: hour must be 0-23, got %d", hour)
	case min < 0 || min > 59:
		return fmt.Errorf("mcu: minute must be 0-59, got %d", min)
	case sec < 0 || sec > 59:
		return fmt.Errorf("mcu: second must be 0-59, got %d", sec)
	default:
		return nil
	}
}

// EX4DateTimeFrame builds WDateAndTime's frame without sending it -
// see buildDateTimeFrame's doc comment for the wire layout. year is
// the full year (e.g. 2026); only the low 2 digits travel on the wire
// (BCD).
func EX4DateTimeFrame(year, mon, dom, dow, hour, min, sec int) ([]byte, error) {
	if err := validateDateTimeFields(year, mon, dom, dow, hour, min, sec); err != nil {
		return nil, err
	}
	return buildDateTimeFrame(ex4WDateAndTime, year, mon, dom, dow, hour, min, sec), nil
}

// SetDateAndTime writes the RTC.
func (m *EX4) SetDateAndTime(year, mon, dom, dow, hour, min, sec int) error {
	frame, err := EX4DateTimeFrame(year, mon, dom, dow, hour, min, sec)
	if err != nil {
		return err
	}
	return sendLegacy(m.p, frame)
}

// EX4DisplayFrame builds SysName's frame without sending it. The
// protocol does not truncate or wrap - see buildTextFrame's doc
// comment - but the LEN byte it writes is `byte(len(text))`, which
// silently wraps for text 256 bytes or longer (byte(256) == 0): the
// frame would then claim a 0-byte payload while still appending all
// 256+ raw text bytes before FB, desyncing the frame structure for
// any receiver. Unlike EX4DateTimeFrame/shutdownFamily's version of
// this bug class, this one IS directly reachable today - the CLI's
// `display`/`-d` command passes arbitrary user-supplied text straight
// through with nothing else bounding its length - so it's checked
// here rather than left as a "not reachable via the current product"
// footnote.
func EX4DisplayFrame(text string) ([]byte, error) {
	if len(text) > 255 {
		return nil, fmt.Errorf("mcu: display text too long (%d bytes, max 255)", len(text))
	}
	return buildTextFrame(ex4SysName, text), nil
}

// Display writes text to the front-panel LCD.
func (m *EX4) Display(text string) error {
	frame, err := EX4DisplayFrame(text)
	if err != nil {
		return err
	}
	return sendLegacy(m.p, frame)
}

// Shutdown/Reset take a runtime value byte - MCUReset in particular
// requires one (`up_send_ctl MCUReset <code>` on the real tool; the
// exact meaning of <code> was not decoded). Not wired into the CLI
// yet; exposed for completeness.
//
// Whatever <code> means semantically is undocumented (so its valid
// range can't be validated), but value lands directly in the frame's
// last payload byte, before FB - if it happened to equal the frame's
// own 0xFA/0xFB markers, the frame itself would be corrupted
// regardless of what the code means (the same class of bug fixed in
// EX4DateTimeFrame/toBCD - see that commit). That much IS a protocol
// invariant, not a semantic guess, so it's checked here.
func (m *EX4) shutdownFamily(c legacyCmd, value byte) error {
	if value == legacyStart || value == legacyEnd {
		return fmt.Errorf("mcu: value 0x%02X would collide with the frame's own start/end marker", value)
	}
	return sendLegacy(m.p, buildByteWriteFrame(c, value))
}

func (m *EX4) DeviceShutdown(value byte) error { return m.shutdownFamily(ex4DeviceShutdown, value) }
func (m *EX4) MCUReset(value byte) error       { return m.shutdownFamily(ex4MCUReset, value) }
