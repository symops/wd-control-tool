package fan

import "github.com/symops/wd-control-tool/pkg/mcu"

// EX2U drives the fan via the Welltrend MCU. Set accepts only the 5
// confirmed duty-cycle steps: 0, 25, 50, 75, 100.
type EX2U struct{ MCU *mcu.EX2U }

func (f EX2U) Set(level int) error { return f.MCU.FanPercent(level) }
