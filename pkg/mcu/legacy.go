package mcu

import "time"

// The EX2/EX4 "legacy" MCU protocol (what up_send_ctl/up_send_daemon
// spoke over /dev/ttyS1). Table-driven, FA...FB framed, no checksum.
// Fully decoded from the real binaries - see SPEC.md, "EX2/EX4
// protocol" for the complete 82-row (EX4) / 56-row (EX2) command
// table this file's ex2.go/ex4.go draw from, and for the worked,
// byte-level examples each frame shape below was confirmed against.
//
// Unlike EX2U's fixed 7-byte frame (ex2u.go), this family has several
// frame shapes depending on the command's "dispatch group":
//   group 1:  fixed, no argument           -> buildFixedFrame
//   group 2:  RTC bulk read/write          -> buildDateTimeFrame (write side)
//   group 3:  single RTC field write       -> buildFixedFrame with runtime value in Const's slot (not yet wired up - out of scope for now)
//   group 8:  shutdown/reset family        -> buildByteWriteFrame
//   group 9:  SysName (free text)          -> buildTextFrame
//   group 24: FanControl (EX4) / WPWMValue (EX2) -> buildValueFrame

const (
	legacyStart = 0xFA
	legacyEnd   = 0xFB
	// up_send_daemon's own transmit loop pace (usleep(10us)) - far
	// smaller than one UART bit time at either board's baud rate, so
	// it is not load-bearing, but reproduced for parity with the
	// vendor implementation. See SPEC.md.
	legacyInterByteDelay = 10 * time.Microsecond
)

// legacyCmd is one row of the vendor command table, extracted
// byte-for-byte from the real binaries' .data section (python
// struct-unpack against the known table address and stride - see
// SPEC.md for the extraction method and the full dump). B4..B7 are
// the frame's header bytes; Const is the payload byte fixed commands
// send as their last byte before FB.
type legacyCmd struct {
	Name           string
	B4, B5, B6, B7 byte
	Const          byte
}

func buildFixedFrame(c legacyCmd) []byte {
	return []byte{legacyStart, c.B4, c.B5, c.B6, c.B7, c.Const, legacyEnd}
}

// buildValueFrame replaces the table's B6 slot with a runtime value -
// confirmed for EX4's FanControl (`up_send_ctl FanControl 3` ->
// FA 02 00 03 00 00 FB, i.e. table B6=00 replaced by the value 3).
func buildValueFrame(c legacyCmd, value byte) []byte {
	return []byte{legacyStart, c.B4, c.B5, value, c.B7, c.Const, legacyEnd}
}

// buildByteWriteFrame replaces the final payload byte (table's Const
// slot) with a runtime value - the shutdown/reset family (dispatch
// group 8): MCUReset actually requires a numeric argument on the
// real tool, contrary to a "no-args" assumption.
func buildByteWriteFrame(c legacyCmd, value byte) []byte {
	return []byte{legacyStart, c.B4, c.B5, c.B6, c.B7, value, legacyEnd}
}

// toBCD packs 0-99 as a BCD byte: ((v/10)<<4)|(v%10). Confirmed in
// up_send_ctl's WDateAndTime handler disassembly.
func toBCD(v int) byte { return byte(((v / 10) << 4) | (v % 10)) }

// buildDateTimeFrame builds WDateAndTime's 13-byte frame:
// FA b4 b5 b6 b7 <sec,min,hour,dow,dom,mon,year BCD> FB. Field order
// on the wire is sec..year - reversed from the year..sec CLI/argument
// order. Confirmed worked example (2026-10-31 Sat 14:05:30):
// FA 01 00 04 07 30 05 14 06 31 10 26 FB.
func buildDateTimeFrame(c legacyCmd, year, mon, dom, dow, hour, min, sec int) []byte {
	return []byte{
		legacyStart, c.B4, c.B5, c.B6, c.B7,
		toBCD(sec), toBCD(min), toBCD(hour), toBCD(dow), toBCD(dom), toBCD(mon), toBCD(year % 100),
		legacyEnd,
	}
}

// buildTextFrame builds SysName's variable-length frame:
// FA b4 b5 b6 LEN <text, raw ASCII, LEN bytes, no NUL> FB. The
// protocol itself does not truncate or paginate at 16 chars - the
// vendor /init script's ">"-truncated strings were the script
// author's own doing, not an enforced wire limit. How the MCU/LCD
// firmware itself handles an over-length payload is untested.
func buildTextFrame(c legacyCmd, text string) []byte {
	b := make([]byte, 0, len(text)+6)
	b = append(b, legacyStart, c.B4, c.B5, c.B6, byte(len(text)))
	b = append(b, text...)
	b = append(b, legacyEnd)
	return b
}

// sendLegacy writes frame with the vendor daemon's own (negligible)
// inter-byte pacing.
func sendLegacy(p *Port, frame []byte) error {
	return p.sendBytes(frame, func() { time.Sleep(legacyInterByteDelay) })
}
