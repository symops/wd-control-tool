package mcu

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

// EX4DateTimeFrame builds WDateAndTime's frame without sending it -
// see buildDateTimeFrame's doc comment for the wire layout. year is
// the full year (e.g. 2026); only the low 2 digits travel on the wire
// (BCD).
func EX4DateTimeFrame(year, mon, dom, dow, hour, min, sec int) []byte {
	return buildDateTimeFrame(ex4WDateAndTime, year, mon, dom, dow, hour, min, sec)
}

// SetDateAndTime writes the RTC.
func (m *EX4) SetDateAndTime(year, mon, dom, dow, hour, min, sec int) error {
	return sendLegacy(m.p, EX4DateTimeFrame(year, mon, dom, dow, hour, min, sec))
}

// EX4DisplayFrame builds SysName's frame without sending it. The
// protocol does not truncate or wrap - see buildTextFrame's doc
// comment.
func EX4DisplayFrame(text string) []byte {
	return buildTextFrame(ex4SysName, text)
}

// Display writes text to the front-panel LCD.
func (m *EX4) Display(text string) error {
	return sendLegacy(m.p, EX4DisplayFrame(text))
}

// Shutdown/Reset take a runtime value byte - MCUReset in particular
// requires one (`up_send_ctl MCUReset <code>` on the real tool; the
// exact meaning of <code> was not decoded). Not wired into the CLI
// yet; exposed for completeness.
func (m *EX4) shutdownFamily(c legacyCmd, value byte) error {
	return sendLegacy(m.p, buildByteWriteFrame(c, value))
}

func (m *EX4) DeviceShutdown(value byte) error { return m.shutdownFamily(ex4DeviceShutdown, value) }
func (m *EX4) MCUReset(value byte) error       { return m.shutdownFamily(ex4MCUReset, value) }
