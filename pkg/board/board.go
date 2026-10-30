// Package board identifies which of the three supported WD My Cloud
// boards wd-control-tool is running on.
//
// The three boards share no single control mechanism: they are grouped
// here only because they used to be driven by the same family of
// vendor tools (fan_control/rtc/led/up_send_ctl, or mcu_ctl). See
// SPEC.md for the full, per-board hardware reference this package's
// siblings (pkg/mcu, pkg/regio, pkg/fan, pkg/led, pkg/rtc) implement.
package board

import "fmt"

// Board is one of the three supported hardware targets. There is no
// generic "all boards" fallback anywhere in this tool: every feature
// is implemented per board because the underlying mechanism differs
// per board (see SPEC.md).
type Board int

const (
	Unknown Board = iota
	EX2U          // WD My Cloud EX2 Ultra (Armada 385, armv7)
	EX2           // WD My Cloud EX2 Gen2 (Kirkwood-class, armv5 ABI today; armv7 silicon)
	EX4           // WD My Cloud EX4 Gen2 (Kirkwood-class, armv5 ABI)
)

func (b Board) String() string {
	switch b {
	case EX2U:
		return "ex2u"
	case EX2:
		return "ex2"
	case EX4:
		return "ex4"
	default:
		return "unknown"
	}
}

// Parse maps a CLI -b/--board value (and its aliases) to a Board.
func Parse(s string) (Board, error) {
	switch s {
	case "ex2u", "ex2-ultra", "ex2ultra":
		return EX2U, nil
	case "ex2":
		return EX2, nil
	case "ex4":
		return EX4, nil
	default:
		return Unknown, fmt.Errorf("unknown board %q (want ex2u, ex2 or ex4)", s)
	}
}

// HasDisplay reports whether the board has a front-panel LCD
// (only EX4's up_send_ctl SysName / wd-control-tool's `display` command
// apply).
func (b Board) HasDisplay() bool { return b == EX4 }

// HasMCU reports whether the board has an MCU reachable over UART at
// all (EX2U: uart1 to the Welltrend 6703F-OG240WT; EX2/EX4: /dev/ttyS1
// to the front-panel MCU). All three do; this exists mainly for
// documentation/readability at call sites.
func (b Board) HasMCU() bool { return b == EX2U || b == EX2 || b == EX4 }
