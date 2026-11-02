// Package rtc syncs the front-panel MCU's RTC with the host's system
// clock, both directions, on the boards where each direction is
// confirmed (EX2/EX4 only - EX2U has no rtc tool call in its own
// vendor /init at all and is out of scope here):
//
//   - SYS -> RTC (the real rtc binary's "-w"): confirmed for both
//     EX4 and EX2 (EX2Writer/EX4Writer).
//   - RTC -> SYS (the real rtc binary's "-r"/"-s"): confirmed for EX2
//     only (EX2Reader) - a live UART capture of the real `rtc -r` on
//     real EX2 hardware decoded RDateAndTime's reply frame (see
//     pkg/mcu's EX2DecodeDateAndTime). EX4's own RDateAndTime reply
//     has not had that same capture done - see ErrEX4ReadNotImplemented.
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
type EX4Writer struct{ MCU *mcu.EX4 }

func (w EX4Writer) Set(t time.Time) error {
	year, mon, dom := t.Date()
	dow := int(t.Weekday()) // 0=Sunday, matching the vendor's own dow convention - confirmed day-by-day for all 7 days, see TestEX4Writer_Set_WeekdayEncoding
	return w.MCU.SetDateAndTime(year, int(mon), dom, dow, t.Hour(), t.Minute(), t.Second())
}

// EX2Writer writes the host's current time to EX2's MCU RTC -
// hardware-confirmed (see pkg/mcu's ex2WDateAndTime/EX2DateTimeFrame).
// dow uses the same 0=Sunday convention as EX4Writer - confirmed for
// EX2 specifically by two independent live captures (`rtc -w` and
// `rtc -r`, both agreeing: Monday read back and written as 1).
type EX2Writer struct{ MCU *mcu.EX2 }

func (w EX2Writer) Set(t time.Time) error {
	year, mon, dom := t.Date()
	dow := int(t.Weekday())
	return w.MCU.SetDateAndTime(year, int(mon), dom, dow, t.Hour(), t.Minute(), t.Second())
}

// EX2Reader reads EX2's MCU RTC - hardware-confirmed (see pkg/mcu's
// EX2DecodeDateAndTime). ok is false (with a nil error) when the MCU
// didn't reply within timeout - the same "normal, not a failure"
// outcome pkg/mcu.Port.recvBytes documents, not something this type
// hides as a zero time.Time.
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
	return time.Date(year, time.Month(mon), dom, hour, min, sec, 0, time.Local), true, nil
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

// ErrEX4ReadNotImplemented: EX4's RDateAndTime reply frame has not
// had the live-capture treatment EX2's got (see pkg/mcu's
// EX2DecodeDateAndTime) - guessing it from EX2's now-confirmed shape
// would violate this project's own rule against guessing MCU frame
// layouts without a live capture of THIS board specifically (EX2 and
// EX4 already disagree on WDateAndTime's own header bytes, so there
// is no reason to assume the reply shape carries over either).
var ErrEX4ReadNotImplemented = fmt.Errorf("rtc: EX4's RTC read framing is not confirmed yet - see pkg/mcu/ex4.go")
