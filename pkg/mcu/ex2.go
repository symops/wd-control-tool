package mcu

import (
	"fmt"
	"time"
)

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

	// ex2NewPWRLed is EX2's chassis POWER LED command - NOT a
	// /dev/mem GPIO poke the way pkg/led's "power" zone assumed (that
	// assumption, from a static libshare.so decompile, was never
	// hardware-confirmed and turned out to be wrong: a live strace of
	// the real `led` tool on real EX2 hardware showed no /dev/mem
	// open at all for `led power ...`, only /dev/ttyS1 + librs232.so
	// - exactly this MCU command, previously listed as one of
	// SPEC.md's "anomalous" unexplained table rows, b4=0x26). See
	// EX2PowerLEDFrames for the confirmed value encoding.
	ex2NewPWRLed = legacyCmd{"NewPWRLed", 0x26, 0x00, 0x00, 0x00, 0x01}
)

// NewPWRLed's value byte (table's B6 slot, replaced by buildValueFrame)
// is a bitmask: one bit selects the color, a separate bit means "on".
// Confirmed via a live strace capturing all 4 combinations of
// color x on/off on real EX2 hardware - see EX2PowerLEDFrames.
const (
	ex2PWRLedBlueBit = 0x01
	ex2PWRLedRedBit  = 0x04
	ex2PWRLedOnFlag  = 0x10
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

// RequestThermal sends ThermalGet's request; as on EX4, this tool
// does not parse the MCU's reply frame for this one (see SPEC.md,
// "unresolved"). RequestFanStatus's reply, unlike ThermalGet's, IS
// decoded - see FanStatusReply/EX2DecodeFanRPM.
func (m *EX2) RequestThermal() error   { return sendLegacy(m.p, buildFixedFrame(ex2ThermalGet)) }
func (m *EX2) RequestFanStatus() error { return sendLegacy(m.p, buildFixedFrame(ex2FanStatusGet)) }

// EX2FanStatusRequestFrame is FanStatusGet's fixed request frame -
// the pure builder -n/--dry-run and -D/--debug use.
func EX2FanStatusRequestFrame() []byte { return buildFixedFrame(ex2FanStatusGet) }

// FanStatusReply sends the FanStatusGet request and returns whatever
// raw bytes the MCU replies with within timeout - possibly none.
// Deliberately returns the UNDECODED bytes; see EX2DecodeFanRPM for
// the decode, layered on top rather than built in, matching this
// project's EX2U TempReply/EX2UDecodeTempCelsius split.
func (m *EX2) FanStatusReply(timeout time.Duration) ([]byte, error) {
	return m.p.sendAndRecv(m.RequestFanStatus, timeout)
}

// EX2DecodeFanRPM decodes a FanStatusGet reply (as returned by
// FanStatusReply) into an RPM value.
//
// Hardware-confirmed: a live strace of the real `fan_control -g 4`
// on real EX2 hardware printed "fan rpm = 5172" while the captured
// reply was `FA 03 09 00 00 3A FB` - byte 5 (0x3A = 58 decimal) is a
// tachometer PERIOD, and 300000/58 = 5172.41, truncating to exactly
// 5172. Cross-confirmed against 6 OTHER independently measured RPM
// values (one per fan level, read back the same way right after
// setting each level - see pkg/fan/ex2.go) with no raw byte captured
// for those: every one of 9090/7317/4411/5357/6521/8333 round-trips
// EXACTLY through this same formula (RPM -> nearest integer period
// -> 300000/period, truncated) - an extraordinary coincidence if the
// formula were wrong, not just a one-off match.
//
// A period of 0 reports RPM 0 rather than dividing by zero - this is
// the arithmetically safe choice (a stopped fan logically reads 0),
// not a hardware claim: no real capture has a period of 0 to confirm
// what the MCU actually sends for a stopped fan.
//
// Scans reply for the matching frame via findFrame rather than
// assuming it starts at offset 0: real hardware was found to
// interleave an unsolicited, unrelated `FA 30 00 00 00 00 FB` frame
// with this one, in any order - see findFrame's own doc comment.
func EX2DecodeFanRPM(reply []byte) (int, error) {
	frame, err := findFrame(reply, ex2FanStatusGet.B4, ex2FanStatusGet.B5)
	if err != nil {
		return 0, err
	}
	period := frame[5]
	if period == 0 {
		return 0, nil
	}
	return 300000 / int(period), nil
}

// EX2PowerLEDFrames builds the two NewPWRLed frames the real `led`
// tool sends for one `led power COLOR STATE` call: first clears the
// OTHER color (value = otherColorBit, no on-flag - itself a genuine
// "turn that color off"), then sets THIS color to the requested
// state (value = colorBit, OR'd with the on-flag if turning on).
//
// Hardware-confirmed by a live strace of the real vendor `led` tool
// on real EX2 hardware for all 4 combinations (red/blue x on/off),
// run after the previously-documented /dev/mem GPIO scheme was found
// to do nothing at all for this zone:
//
//	blue on:  FA 26 00 04 00 01 FB, FA 26 00 11 00 01 FB
//	blue off: FA 26 00 04 00 01 FB, FA 26 00 01 00 01 FB
//	red on:   FA 26 00 01 00 01 FB, FA 26 00 14 00 01 FB
//	red off:  FA 26 00 01 00 01 FB, FA 26 00 04 00 01 FB
//
// Only on/off were captured - "blinking" has no confirmed frame (see
// SPEC.md's "EX2/EX4 protocol", NewPWRLed, and this project's own
// house rule against guessing one).
func EX2PowerLEDFrames(color string, on bool) ([2][]byte, error) {
	var colorBit, otherBit byte
	switch color {
	case "blue":
		colorBit, otherBit = ex2PWRLedBlueBit, ex2PWRLedRedBit
	case "red":
		colorBit, otherBit = ex2PWRLedRedBit, ex2PWRLedBlueBit
	default:
		return [2][]byte{}, fmt.Errorf("mcu: unknown EX2 power LED color %q", color)
	}
	value := colorBit
	if on {
		value |= ex2PWRLedOnFlag
	}
	return [2][]byte{
		buildValueFrame(ex2NewPWRLed, otherBit),
		buildValueFrame(ex2NewPWRLed, value),
	}, nil
}

// ex2PowerLEDInterFrameDelay is the pause between PowerLED's two
// frames - hardware-confirmed necessary, not cosmetic: sending both
// back-to-back (as this tool originally did) was intermittently
// unreliable on real EX2 hardware (confirmed: switching color, or
// even a fresh on from off, sometimes needed more than one attempt,
// and a failed switch typically just went dark rather than ending up
// either color - the "set" frame getting lost while the MCU was
// presumably still processing "clear"). A live strace of one
// successful real `led power red on` run showed the vendor tool
// itself pausing ~400ms between the two frames (39.396806 -> last
// byte of "clear"; 39.797133 -> first byte of "set") - not 10us
// (up_send_daemon's otherwise-documented inter-BYTE pacing) and not
// back-to-back.
const ex2PowerLEDInterFrameDelay = 400 * time.Millisecond

// PowerLED sends EX2PowerLEDFrames's two frames, pausing
// ex2PowerLEDInterFrameDelay between them - see that constant's doc
// comment for why the pause is load-bearing, not cosmetic.
func (m *EX2) PowerLED(color string, on bool) error {
	frames, err := EX2PowerLEDFrames(color, on)
	if err != nil {
		return err
	}
	if err := sendLegacy(m.p, frames[0]); err != nil {
		return err
	}
	time.Sleep(ex2PowerLEDInterFrameDelay)
	return sendLegacy(m.p, frames[1])
}
