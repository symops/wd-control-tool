package mcu

// EX2's legacy MCU, /dev/ttyS1, 115200 8N1 (note: different baud from
// EX4's 19200 - confirmed, do not assume parity).
//
// EX2's command table is NOT "EX4 minus the display" - header byte
// VALUES differ even for same-named commands, and some commands are
// structured differently (fan control is 3 separate no-argument
// commands here, not one leveled command; RDateAndTime/WDateAndTime
// share one dispatch group instead of being split across groups 1/2
// like EX4). See SPEC.md for the full 56-row table dump.
var (
	ex2FanStop      = legacyCmd{"FanStop", 0x02, 0x00, 0x00, 0x00, 0x00}
	ex2FanHalf      = legacyCmd{"FanHalf", 0x02, 0x00, 0x01, 0x00, 0x00}
	ex2FanFull      = legacyCmd{"FanFull", 0x02, 0x00, 0x02, 0x00, 0x00}
	ex2DeviceReady  = legacyCmd{"DeviceReady", 0x03, 0x01, 0x00, 0x00, 0x00}
	ex2ThermalGet   = legacyCmd{"ThermalGet", 0x03, 0x08, 0x00, 0x00, 0x00}
	ex2FanStatusGet = legacyCmd{"FanStatusGet", 0x03, 0x09, 0x00, 0x00, 0x00}
)

// EX2 is a legacy-protocol client bound to EX2's command table.
//
// NOTE: EX2's RDateAndTime/WDateAndTime are intentionally NOT exposed
// here yet. They share one dispatch group in the real binary (unlike
// EX4's split groups 1/2), and this tool has no confirmed worked
// byte example for EX2's write-side framing the way it does for
// EX4's (see ex4.go's SetDateAndTime) - only the table's header bytes
// are known. Guessing the payload layout from EX4's by analogy would
// violate this project's own rule (see SPEC.md / memory
// ex2u-mcu-uart-protocol-confirmed): never guess an MCU frame layout
// without a live capture. rtc support for EX2 needs that capture
// first.
type EX2 struct{ p *Port }

func NewEX2(p *Port) *EX2 { return &EX2{p: p} }

// FanStop/FanHalf/FanFull are EX2's only 3 fan states - there is no
// leveled FanControl command on this board's legacy protocol (EX2's
// `fan_control -f N` for N>0 instead writes direct SoC registers, see
// pkg/fan/ex2.go - the MCU is not involved in setting EX2's fan
// speed at all, only in reading sensors).
func (m *EX2) FanStop() error { return sendLegacy(m.p, buildFixedFrame(ex2FanStop)) }
func (m *EX2) FanHalf() error { return sendLegacy(m.p, buildFixedFrame(ex2FanHalf)) }
func (m *EX2) FanFull() error { return sendLegacy(m.p, buildFixedFrame(ex2FanFull)) }

func (m *EX2) DeviceReady() error { return sendLegacy(m.p, buildFixedFrame(ex2DeviceReady)) }

// RequestThermal/RequestFanStatus send the sensor-read requests; as on
// EX4, this tool does not yet parse the MCU's reply frame (see
// SPEC.md, "unresolved").
func (m *EX2) RequestThermal() error   { return sendLegacy(m.p, buildFixedFrame(ex2ThermalGet)) }
func (m *EX2) RequestFanStatus() error { return sendLegacy(m.p, buildFixedFrame(ex2FanStatusGet)) }
