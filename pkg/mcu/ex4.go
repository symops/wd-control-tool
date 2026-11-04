package mcu

import (
	"fmt"
	"time"
)

// EX4's legacy MCU, /dev/ttyS1, 19200 8N1.
//
// Table rows below are exact byte-for-byte extractions from the real
// up_send_ctl binary's command table (82 rows total; only the rows
// this tool currently wires up are listed here - see SPEC.md for the
// full dump). Do not hand-edit these constants without re-extracting
// from the binary: they are not derived from any documented spec.
var (
	ex4FanControl   = legacyCmd{"FanControl", 0x02, 0x00, 0x00, 0x00, 0x00}
	ex4RDateAndTime = legacyCmd{"RDateAndTime", 0x01, 0x00, 0x03, 0x01, 0x00}
	ex4WDateAndTime = legacyCmd{"WDateAndTime", 0x01, 0x00, 0x04, 0x07, 0x00}

	// ex4RDateAndTimeReplyHeader: the real RDateAndTime REPLY's own
	// header, as 5 bytes (FA b4 b5 b6 b7) - confirmed via a live
	// strace of the real `rtc -r` on real EX4 hardware. NOT the same
	// as ex4RDateAndTime's own request header above (b7 differs: the
	// request's own b7=01 is the table's value, used unmodified; the
	// reply's b7 is 07 instead - the same byte as WDateAndTime's own
	// table b7, not RDateAndTime's). Unlike EX2 (whose RDateAndTime
	// reply echoes its OWN request header verbatim), EX4's reply
	// header does NOT match its own request - see EX4DecodeDateAndTime
	// for the full captured example.
	ex4RDateAndTimeReplyHeader = []byte{0x01, 0x00, 0x03, 0x07}
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

// EX4DateTimeRequestFrame is RDateAndTime's fixed request frame - the
// pure builder -n/--dry-run and -D/--debug use.
func EX4DateTimeRequestFrame() []byte { return buildFixedFrame(ex4RDateAndTime) }

// RequestDateAndTime sends RDateAndTime's request.
func (m *EX4) RequestDateAndTime() error {
	return sendLegacy(m.p, buildFixedFrame(ex4RDateAndTime))
}

// DateAndTimeReply sends RDateAndTime's request and returns whatever
// raw bytes the MCU replies with within timeout - possibly none. Same
// split as EX2's own (undecoded bytes here, EX4DecodeDateAndTime
// layered on top).
func (m *EX4) DateAndTimeReply(timeout time.Duration) ([]byte, error) {
	return m.p.sendAndRecv(m.RequestDateAndTime, timeout)
}

// EX4DecodeDateAndTime decodes an RDateAndTime reply (as returned by
// DateAndTimeReply) into year/mon/dom/dow/hour/min/sec.
//
// Hardware-confirmed: a live strace of the real `rtc -r` on real EX4
// hardware, while it printed "rtc: RTC time = 2026/11/4 Tue
// 20:52:57", captured the reply
// FA 01 00 03 07 57 52 20 03 04 11 26 FB - NOT the request's own
// header echoed back (unlike EX2's RDateAndTime) - see
// ex4RDateAndTimeReplyHeader's own doc comment for the b7 mismatch -
// followed by the same 7-field BCD payload order WDateAndTime's
// write side uses (sec,min,hour,dow,dom,mon,year - see
// EX4DateTimeFrame), confirmed field-by-field against the printed
// date: sec=0x57->57, min=0x52->52, hour=0x20->20, dom=0x04->4,
// mon=0x11->11, year=0x26->2026 - exact.
//
// dow=0x03->3 is the ONE field that does NOT match EX2's convention:
// the vendor tool printed "Tue" for a stored dow of 3, but 2026-11-04
// is actually a Wednesday (confirmed: 2026-11-01 is a Sunday, used
// elsewhere in this project's own tests/SPEC.md) - i.e. the vendor's
// own weekday-name lookup expects 1=Sunday..7=Saturday, NOT EX2's
// (confirmed, hardware-verified) 0=Sunday..6=Saturday. The raw byte
// is read back completely unchanged from what was written moments
// earlier in the same capture (confirmed: the `-D` write frame's own
// dow byte was also 03) - so this is purely a difference in what the
// VENDOR firmware expects on the wire, not a storage-side transform,
// and the fix belongs on the write side (see rtc.EX4Writer.Set).
// EX4DecodeDateAndTime itself returns the RAW dow byte as-is (0-6,
// same range as EX2's decoder) - converting it to match the vendor's
// 1-7 display convention, if ever needed, belongs to the caller, the
// same way EX2Reader.Read doesn't do any such conversion either.
//
// Scans reply for the matching frame via findFrame rather than
// assuming it starts at offset 0 - same reasoning as EX2's own
// decoder (unsolicited noise frames can interleave).
func EX4DecodeDateAndTime(reply []byte) (year, mon, dom, dow, hour, min, sec int, err error) {
	frame, err := findFrame(reply, ex4RDateAndTimeReplyHeader, 13)
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
	case dow < 0 || dow > 7:
		// 0-7, not 0-6: EX2 uses 0=Sunday..6=Saturday, EX4 uses
		// 1=Sunday..7=Saturday (both hardware-confirmed, see
		// rtc.EX2Writer.Set/EX4Writer.Set's own doc comments for how
		// EX4's was found to differ) - this validator is shared by
		// both boards' DateTimeFrame builders, so it accepts the
		// union of both ranges rather than knowing which board called
		// it; each Writer only ever produces its own board's valid
		// value either way.
		return fmt.Errorf("mcu: day of week must be 0-7, got %d", dow)
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
//
// Same reasoning applies to the payload bytes themselves, not just
// the LEN byte: this tool has never decoded the MCU firmware's own
// receive-side parsing loop (see SPEC.md, "unresolved" #1), so
// whether it trusts LEN for a fixed-size read or re-scans for FB is
// unknown - and a literal 0xFA/0xFB byte inside text would corrupt
// the frame under either interpretation (it either looks like a
// second start marker, or a premature end marker). `--display`'s
// README contract is "taken verbatim, even if it looks like a flag",
// which is about shell-level flag-sniffing, not about bytes that
// alias the wire protocol's own markers - os.Args bytes aren't
// guaranteed valid UTF-8 (e.g. `--display $'\xfa\xfb'`), so this is
// reachable with a deliberately crafted argument, the same way the
// BCD and shutdown-byte collisions were reachable with a crafted
// field value.
func EX4DisplayFrame(text string) ([]byte, error) {
	if len(text) > 255 {
		return nil, fmt.Errorf("mcu: display text too long (%d bytes, max 255)", len(text))
	}
	// The LEN byte itself - byte(len(text)), written just before the
	// text - is just as capable of colliding as any payload byte: a
	// 250 or 251-byte text (0xFA/0xFB) produces a LEN byte that IS a
	// frame marker, even if every character in the text is perfectly
	// ordinary. Found by FuzzEX4DisplayFrame_NeverEmitsMarkerBytesInPayload
	// on its very first real run (a 250-byte all-'0' string), not by
	// manual review - exactly the kind of boundary a human reviewer
	// checking "does any text byte collide" tends to walk right past
	// because the LEN byte isn't a text byte at all.
	if n := byte(len(text)); n == legacyStart || n == legacyEnd {
		return nil, fmt.Errorf("mcu: display text length %d (0x%02X) would make the frame's own LEN byte collide with its start/end marker", len(text), n)
	}
	for i := 0; i < len(text); i++ {
		if b := text[i]; b == legacyStart || b == legacyEnd {
			return nil, fmt.Errorf("mcu: display text byte %d (0x%02X) would collide with the frame's own start/end marker", i, b)
		}
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
