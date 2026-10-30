// Package rtc writes the host's system time into the front-panel
// MCU's RTC (EX2/EX4 only - the real rtc binary's "-w" direction,
// SYS -> RTC). EX2U has no rtc tool call in its own vendor /init at
// all and is out of scope here.
//
// IMPORTANT, READ BEFORE EXTENDING: this package can only WRITE the
// RTC, not read it back. The vendor's RTC *read* path (and the
// temperature/fan-status reads in pkg/fan) depends on parsing the
// MCU's reply frames over UART, which this project has not decoded -
// the vendor's own up_read_daemon/xmldbc machinery that did this was
// never reverse-engineered past "it exists and publishes named
// results somewhere" (see SPEC.md, "unresolved"). That also means
// there is no ack confirmation that the MCU actually received a
// write - Set() is fire-and-forget, same risk profile as sending a
// command the vendor tools would normally retry/verify.
//
// The real `rtc -s` (RTC -> SYS, the direction actually used at boot
// in both EX2 and EX4's vendor /init) is NOT reproducible yet for
// this reason - it needs a read. Decoding the MCU's reply framing
// (ideally via a live UART capture, the same way EX2U's protocol was
// confirmed) is the next step before that command can be added.
package rtc

import (
	"fmt"
	"time"

	"github.com/symops/wd-control-tool/pkg/mcu"
)

// EX4Writer writes the host's current time to EX4's MCU RTC.
type EX4Writer struct{ MCU *mcu.EX4 }

func (w EX4Writer) Set(t time.Time) error {
	year, mon, dom := t.Date()
	dow := int(t.Weekday()) // 0=Sunday, matching the vendor's own dow convention (unconfirmed beyond that it's a 0-6 range - see SPEC.md)
	return w.MCU.SetDateAndTime(year, int(mon), dom, dow, t.Hour(), t.Minute(), t.Second())
}

// EX2 is intentionally not implemented: EX2's RDateAndTime/
// WDateAndTime share one dispatch group whose write-side frame
// layout this project does not have a confirmed worked example for
// (see pkg/mcu/ex2.go's doc comment). Guessing it would violate this
// project's own rule against guessing MCU frame layouts.
var ErrEX2NotImplemented = fmt.Errorf("rtc: EX2's RTC write framing is not confirmed yet - see pkg/mcu/ex2.go")
