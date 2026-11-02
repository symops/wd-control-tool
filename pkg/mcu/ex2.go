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

	// ex2RDateAndTime/ex2WDateAndTime: EX2's shared "group 2" RTC
	// commands - CONFIRMED via live strace of the real `rtc -r`/`rtc
	// -w` on real EX2 hardware (previously unconfirmed - see SPEC.md's
	// now-resolved "unresolved" item 2).
	//
	// WDateAndTime's B7 here (0x07) is a CORRECTION to SPEC.md's own
	// table dump, which recorded 0x01 (the same as RDateAndTime's row)
	// for this field: the real wire capture of `rtc -w` shows the
	// actual transmitted byte at that position is 0x07, not 0x01 -
	// same byte-for-byte shape as EX4's WDateAndTime
	// (FA b4 b5 b6 b7 <7 BCD bytes> FB via buildDateTimeFrame), just a
	// different b4/b5/b6/b7. RDateAndTime's own B7=0x01 WAS confirmed
	// correct by the same capture (its 7-byte request frame matches
	// the table exactly - it uses buildFixedFrame, not
	// buildDateTimeFrame, since a read has no payload to send).
	ex2RDateAndTime = legacyCmd{"RDateAndTime", 0x01, 0x08, 0x01, 0x01, 0x00}
	ex2WDateAndTime = legacyCmd{"WDateAndTime", 0x01, 0x08, 0x02, 0x07, 0x00}

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
	frame, err := findFrame(reply, []byte{ex2FanStatusGet.B4, ex2FanStatusGet.B5}, 7)
	if err != nil {
		return 0, err
	}
	period := frame[5]
	if period == 0 {
		return 0, nil
	}
	return 300000 / int(period), nil
}

// EX2DateTimeRequestFrame is RDateAndTime's fixed request frame -
// the pure builder -n/--dry-run and -D/--debug use.
func EX2DateTimeRequestFrame() []byte { return buildFixedFrame(ex2RDateAndTime) }

// RequestDateAndTime sends RDateAndTime's request.
func (m *EX2) RequestDateAndTime() error {
	return sendLegacy(m.p, buildFixedFrame(ex2RDateAndTime))
}

// DateAndTimeReply sends RDateAndTime's request and returns whatever
// raw bytes the MCU replies with within timeout - possibly none.
// Deliberately returns the UNDECODED bytes; see EX2DecodeDateAndTime
// for the decode, same split as FanStatusReply/EX2DecodeFanRPM.
//
// A fixed pre-request delay was tried here first (ex2RTCRequestSettleDelay,
// 300ms, since removed) after real-hardware testing found chained
// rapid-fire RDateAndTime requests (six `-r r` actions, zero
// think-time apart) getting corrupted/stale replies after the first
// one. It had ZERO measurable effect - a repeat of the exact same
// test produced byte-for-byte identical garbled output before and
// after adding it, ruling out "the MCU just needs more settle time".
// The real cause turned out to be on THIS side, not the MCU's: a bug
// in Port.recvBytes (pkg/mcu/serial.go) that could hand one request's
// reply to a different, already-abandoned read attempt from an
// earlier one - fixed there directly (see readPollInterval's own doc
// comment for the full story). cmd/wd-control-tool/main.go's
// buildRTCReadAction also retries a decode failure a few times as
// defense in depth, since this protocol still has no checksum or
// acknowledgement of its own either way.
func (m *EX2) DateAndTimeReply(timeout time.Duration) ([]byte, error) {
	return m.p.sendAndRecv(m.RequestDateAndTime, timeout)
}

// EX2DecodeDateAndTime decodes an RDateAndTime reply (as returned by
// DateAndTimeReply) into year/mon/dom/dow/hour/min/sec.
//
// Hardware-confirmed: a live strace of the real `rtc -r` on real EX2
// hardware, while it printed "rtc: RTC time = 2026/11/2 Mon
// 13:39:43", captured the reply
// FA 01 08 01 01 43 39 13 01 02 11 26 FB - the request's own 5-byte
// header (FA b4 b5 b6 b7) echoed back verbatim, followed by the SAME
// 7-field BCD payload order WDateAndTime's write side uses
// (sec,min,hour,dow,dom,mon,year - see EX2DateTimeFrame), confirmed
// field-by-field against the printed date: sec=0x43->43, min=0x39->39,
// hour=0x13->13, dow=0x01 (Monday, same 0-6 convention as the write
// side), dom=0x02->2, mon=0x11->11, year=0x26->2026 - an exact match
// across all 7 fields, not a partial or coincidental one.
//
// Scans reply for the matching frame via findFrame rather than
// assuming it starts at offset 0 - real EX2 hardware was found to
// interleave the same unsolicited `FA 30 00 00 00 00 FB` frame seen
// around FanStatusGet's reply (see findFrame's own doc comment).
func EX2DecodeDateAndTime(reply []byte) (year, mon, dom, dow, hour, min, sec int, err error) {
	frame, err := findFrame(reply, []byte{ex2RDateAndTime.B4, ex2RDateAndTime.B5, ex2RDateAndTime.B6, ex2RDateAndTime.B7}, 13)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, err
	}
	sec = fromBCD(frame[5])
	min = fromBCD(frame[6])
	hour = fromBCD(frame[7])
	dow = fromBCD(frame[8])
	dom = fromBCD(frame[9])
	mon = fromBCD(frame[10])
	year = 2000 + fromBCD(frame[11])
	return year, mon, dom, dow, hour, min, sec, nil
}

// EX2DateTimeFrame builds WDateAndTime's frame without sending it -
// see buildDateTimeFrame's doc comment for the wire layout. year is
// the full year (e.g. 2026); only the low 2 digits travel on the
// wire (BCD). Mirrors EX4DateTimeFrame exactly (same validation, same
// field order) - see ex2WDateAndTime's own doc comment for how its
// header differs from EX4's.
func EX2DateTimeFrame(year, mon, dom, dow, hour, min, sec int) ([]byte, error) {
	if err := validateDateTimeFields(year, mon, dom, dow, hour, min, sec); err != nil {
		return nil, err
	}
	return buildDateTimeFrame(ex2WDateAndTime, year, mon, dom, dow, hour, min, sec), nil
}

// ex2RTCVerifyAttempts/ex2RTCVerifyReadTimeout: SetDateAndTime
// write-then-verify loop.
//
// Hardware-confirmed root cause (see BUILDING.md): a RDateAndTime
// read immediately following a WDateAndTime write can come back with
// sec/min right (read live off the RTC chip every time) but
// hour/dow/dom/mon/year FROZEN at a stale snapshot - confirmed by a
// byte-level trace (this project's own WD_MCU_TRACE, not strace -
// wrapping every syscall in strace made the bug stop reproducing
// entirely, so a lightweight in-process trace was added instead)
// showing the EXACT SAME wrong hour/dow/dom/mon/year bytes returned
// by two separate reads 1.3s apart, across two separate process
// invocations, with sec advancing normally between them - while this
// tool's own flush/send/recv sequence around both reads was
// completely clean (0 bytes pending at flush, one well-formed frame,
// matching header) - ruling out anything on this tool's own side
// (an unread write ack contaminating the next read, in particular).
// The SAME trace shows the very next WDateAndTime write immediately
// clears the freeze - the following read is correct again. This is
// therefore a genuine bug in the MCU firmware's own RTC-read
// composition, not a transport/timing issue this tool can pace or
// settle its way around: a flat 300ms pause (in two different
// placements) and inter-byte write pacing up to 10ms/byte (1000x the
// vendor's own 10us) were both tried on real hardware and had zero
// effect on the failure rate.
//
// What DOES work, per the same trace: a write clears the freeze. So
// rather than guess at a delay, SetDateAndTime reads straight back
// what it just wrote (same process, so it knows the exact expected
// value - not guessing a "plausible" date the way a standalone read
// would have to, which is why this doesn't reintroduce the
// implausible-year sanity check already rejected once: that one
// would have misdiagnosed a genuinely dead RTC battery as
// corruption, this one only ever compares against a value this same
// call just sent) and re-sends the write if the readback doesn't
// match, up to ex2RTCVerifyAttempts times.
//
// ex2RTCVerifyReadTimeout matches cmd/wd-control-tool/main.go's own
// ex2RTCReplyTimeout (not shared as a single constant - different
// packages, same reasoning: how long a RDateAndTime reply can take).
const (
	ex2RTCVerifyAttempts    = 3
	ex2RTCVerifyReadTimeout = 2 * time.Second
)

// SetDateAndTime writes EX2's RTC, then reads it straight back to
// verify the write actually took - see ex2RTCVerifyAttempts' own doc
// comment for the hardware bug this guards against and why a plain
// fire-and-forget send isn't enough on this board.
func (m *EX2) SetDateAndTime(year, mon, dom, dow, hour, min, sec int) error {
	frame, err := EX2DateTimeFrame(year, mon, dom, dow, hour, min, sec)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 1; attempt <= ex2RTCVerifyAttempts; attempt++ {
		if err := sendLegacy(m.p, frame); err != nil {
			return err
		}

		reply, err := m.DateAndTimeReply(ex2RTCVerifyReadTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		if len(reply) == 0 {
			lastErr = fmt.Errorf("mcu: RTC write verify: no reply from MCU within %v", ex2RTCVerifyReadTimeout)
			continue
		}
		gotYear, gotMon, gotDom, _, gotHour, gotMin, _, err := EX2DecodeDateAndTime(reply)
		if err != nil {
			lastErr = err
			continue
		}
		// sec is deliberately not compared: real time keeps ticking
		// between the write and this readback, so an exact match on
		// sec isn't expected even when the write fully took. dow
		// isn't compared either - it's derived from year/mon/dom by
		// every caller (see EX2Writer.Set), not an independent field
		// worth cross-checking on its own.
		if gotYear == year && gotMon == mon && gotDom == dom && gotHour == hour && gotMin == min {
			return nil
		}
		lastErr = fmt.Errorf("mcu: RTC write did not take effect: wrote %04d-%02d-%02d %02d:%02d, read back %04d-%02d-%02d %02d:%02d",
			year, mon, dom, hour, min, gotYear, gotMon, gotDom, gotHour, gotMin)
	}
	return lastErr
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
