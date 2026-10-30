package fan

import "github.com/symops/wd-control-tool/pkg/mcu"

// EX4 drives the fan via the legacy MCU protocol. Set accepts 0-4
// (Stop, Low, Medium, High, Highest - fan_control -f N's own
// documented range; confirmed as a direct passthrough, see SPEC.md).
type EX4 struct{ MCU *mcu.EX4 }

func (f EX4) Set(level int) error { return f.MCU.FanLevel(level) }
