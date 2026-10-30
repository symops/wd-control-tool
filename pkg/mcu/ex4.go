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

// FanLevel sets the fan via the MCU - EX4 has no direct-register fan
// path at all (unlike EX2, see pkg/fan/ex2.go). Levels: 0 Stop, 1 Low,
// 2 Medium, 3 High, 4 Highest (fan_control -f N's own documented
// range; confirmed as a straight passthrough, not remapped).
func (m *EX4) FanLevel(level int) error {
	if level < 0 || level > 4 {
		return errUnsupportedFanLevel(level)
	}
	return sendLegacy(m.p, buildValueFrame(ex4FanControl, byte(level)))
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

// SetDateAndTime writes the RTC. year is the full year (e.g. 2026);
// only the low 2 digits travel on the wire (BCD).
func (m *EX4) SetDateAndTime(year, mon, dom, dow, hour, min, sec int) error {
	return sendLegacy(m.p, buildDateTimeFrame(ex4WDateAndTime, year, mon, dom, dow, hour, min, sec))
}

// Display writes text to the front-panel LCD. The protocol does not
// truncate or wrap - see buildTextFrame's doc comment.
func (m *EX4) Display(text string) error {
	return sendLegacy(m.p, buildTextFrame(ex4SysName, text))
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
