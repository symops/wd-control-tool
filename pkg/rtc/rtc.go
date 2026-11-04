// Package rtc syncs the front-panel MCU's RTC with the host's system
// clock, both directions, on the boards where each direction is
// confirmed (EX2/EX4 only - EX2U has no rtc tool call in its own
// vendor /init at all and is out of scope here):
//
//   - SYS -> RTC (the real rtc binary's "-w"): confirmed for both
//     EX4 and EX2 (EX2Writer/EX4Writer).
//   - RTC -> SYS (the real rtc binary's "-r"/"-s"): confirmed for both
//     EX2 (EX2Reader) and EX4 (EX4Reader) - a live UART capture of the
//     real `rtc -r` on each board decoded RDateAndTime's reply frame
//     (see pkg/mcu's EX2DecodeDateAndTime/EX4DecodeDateAndTime).
//
// The MCU's RTC stores UTC, not local time - confirmed on real EX2
// hardware (`date` vs. the vendor `ex2.sh "rtc -w"`'s own "system
// time:" line, run back to back: exactly 3 hours apart, i.e. the
// vendor tool applies NO timezone adjustment at all). Every Writer
// here converts its input to UTC before extracting fields, and
// EX2Reader's own return value is tagged time.UTC, not time.Local -
// do not "fix" either without re-confirming this on real hardware.
//
// There is no ack confirmation that a write was actually received -
// Set() is fire-and-forget on both boards, same risk profile as
// sending any other command the vendor tools would normally
// retry/verify.
package rtc

import (
	"fmt"
	"time"

	"github.com/symops/wd-control-tool/pkg/mcu"
	"golang.org/x/sys/unix"
)

// EX4Writer writes the host's current time to EX4's MCU RTC.
//
// t is converted to UTC before its fields are extracted - the RTC
// stores UTC, not local time, confirmed on real EX2 hardware by
// running `date` and the vendor `rtc -w` back to back in one
// session: `date` printed the host's local (+03) time while the
// vendor tool's own "system time:" line printed exactly 3 hours
// earlier - i.e. UTC, with no timezone adjustment at all. Passing a
// local-zoned t straight to Date()/Hour() (this type's original,
// un-fixed behavior) silently wrote the wrong absolute moment
// whenever the host's timezone wasn't already UTC.
type EX4Writer struct{ MCU *mcu.EX4 }

// dow is Go's own Weekday() PLUS ONE (1=Sunday..7=Saturday), NOT
// EX2's 0=Sunday..6=Saturday convention - confirmed wrong the hard
// way, not assumed: an earlier version of this method used the plain
// 0-indexed value (matching EX2, and this method's own doc comment
// claimed it was "confirmed day-by-day for all 7 days" - that claim
// only ever checked the byte THIS method wrote, never the vendor's
// own interpretation of it, since EX4 had no read support to check
// against). A live capture of `rtc -w` immediately followed by `rtc
// -r` on real EX4 hardware caught it: wrote dow=3 for 2026-11-04 (a
// Wednesday - 2026-11-01 is a Sunday, used elsewhere in this
// project's own tests), read back the SAME raw byte (3, unchanged -
// confirmed not a storage-side transform), yet the vendor's own `rtc
// -r` printed "Tue" for it - only consistent with the vendor's own
// weekday-name table expecting 1=Sunday, not 0=Sunday. +1 here is
// the fix on the write side, not a guess: it's the one value that
// makes EX4's own read-back agree with the real calendar date.
func (w EX4Writer) Set(t time.Time) error {
	t = t.UTC()
	year, mon, dom := t.Date()
	dow := int(t.Weekday()) + 1
	return w.MCU.SetDateAndTime(year, int(mon), dom, dow, t.Hour(), t.Minute(), t.Second())
}

// EX2Writer writes the host's current time to EX2's MCU RTC -
// hardware-confirmed (see pkg/mcu's ex2WDateAndTime/EX2DateTimeFrame).
// dow uses the same 0=Sunday convention as EX4Writer - confirmed for
// EX2 specifically by two independent live captures (`rtc -w` and
// `rtc -r`, both agreeing: Monday read back and written as 1). t is
// converted to UTC first - see EX4Writer's own doc comment for why
// (confirmed the same way for EX2 specifically: `date` vs. `ex2.sh
// "rtc -w"`'s own "system time:" line, exactly 3 hours apart).
type EX2Writer struct{ MCU *mcu.EX2 }

func (w EX2Writer) Set(t time.Time) error {
	t = t.UTC()
	year, mon, dom := t.Date()
	dow := int(t.Weekday())
	return w.MCU.SetDateAndTime(year, int(mon), dom, dow, t.Hour(), t.Minute(), t.Second())
}

// EX2Reader reads EX2's MCU RTC - hardware-confirmed (see pkg/mcu's
// EX2DecodeDateAndTime). The returned Time is tagged time.UTC, not
// time.Local - the decoded fields ARE UTC (see EX4Writer's own doc
// comment for how that was confirmed), so tagging them time.Local
// would silently misinterpret the RTC's real moment by the host's
// own timezone offset - exactly the bug this type's write-side
// counterparts (EX2Writer/EX4Writer) had before being fixed the same
// way. Callers that want to DISPLAY this in local time can call
// .Local() themselves; matching the vendor `rtc -r`'s own unconverted
// "RTC time = ..." wording (see cmd/wd-control-tool/main.go's
// buildRTCReadAction) means NOT doing that by default here.
//
// ok is false (with a nil error) when the MCU didn't reply within
// timeout - the same "normal, not a failure" outcome
// pkg/mcu.Port.recvBytes documents, not something this type hides as
// a zero time.Time.
type EX2Reader struct{ MCU *mcu.EX2 }

func (r EX2Reader) Read(timeout time.Duration) (t time.Time, ok bool, err error) {
	reply, err := r.MCU.DateAndTimeReply(timeout)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(reply) == 0 {
		return time.Time{}, false, nil
	}
	year, mon, dom, _, hour, min, sec, err := mcu.EX2DecodeDateAndTime(reply)
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Date(year, time.Month(mon), dom, hour, min, sec, 0, time.UTC), true, nil
}

// EX4Reader reads EX4's MCU RTC - hardware-confirmed (see pkg/mcu's
// EX4DecodeDateAndTime). Same UTC tagging as EX2Reader, same reasoning.
//
// Read returns the RAW dow byte EX4DecodeDateAndTime decoded
// (0-6, like EX2's) buried inside the returned time.Time's own
// Weekday() - time.Date computes Weekday() from year/mon/dom itself,
// ignoring the wire's dow field entirely, so EX4's 1-7 vs EX2's 0-6
// convention never surfaces here at all; it only ever matters to
// EX4Writer.Set, which has to produce it.
type EX4Reader struct{ MCU *mcu.EX4 }

func (r EX4Reader) Read(timeout time.Duration) (t time.Time, ok bool, err error) {
	reply, err := r.MCU.DateAndTimeReply(timeout)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(reply) == 0 {
		return time.Time{}, false, nil
	}
	year, mon, dom, _, hour, min, sec, err := mcu.EX4DecodeDateAndTime(reply)
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Date(year, time.Month(mon), dom, hour, min, sec, 0, time.UTC), true, nil
}

// SetSystemTime sets the host's own system clock to t - the second
// half of the real `rtc -s` (RTC -> SYS): a caller first reads the
// MCU's RTC (EX2Reader.Read, or the CLI's own equivalent), then
// applies the result here. A real, hardware/OS-affecting side effect
// (changes the whole machine's view of "now") - deliberately NOT
// exercised by this project's own automated tests, the same way
// pkg/regio.Open's real register writes aren't: no automated test
// should change the clock of whatever machine runs `go test`.
func SetSystemTime(t time.Time) error {
	tv := unix.NsecToTimeval(t.UnixNano())
	if err := unix.Settimeofday(&tv); err != nil {
		return fmt.Errorf("rtc: set system time: %w", err)
	}
	return nil
}
