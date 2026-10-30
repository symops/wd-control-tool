// Package led implements EX2/EX4's LED control - confirmed to be
// direct /dev/mem register pokes against a Marvell-style SoC GPIO
// controller at physical base 0xF1000000, NOT the MCU/UART channel,
// NOT sysfs, NOT I2C. See SPEC.md, "LED (EX2/EX4)".
//
// EX2U's LED (a single MCU-driven status LED, plus plain
// kernel-LED-class hd0/hd1 bay LEDs that are out of scope for this
// tool) is handled separately via pkg/mcu's EX2U client, not here.
package led

import "github.com/symops/wd-control-tool/pkg/regio"

// Zone is a physical LED group. EX2 only has Power/HD0/HD1; EX4 adds
// HD2/HD3.
type Zone int

const (
	Power Zone = iota
	HD0
	HD1
	HD2
	HD3
)

func (z Zone) String() string {
	switch z {
	case Power:
		return "power"
	case HD0:
		return "hd0"
	case HD1:
		return "hd1"
	case HD2:
		return "hd2"
	case HD3:
		return "hd3"
	default:
		return "unknown"
	}
}

type Color int

const (
	Red Color = iota
	Blue
)

func (c Color) String() string {
	switch c {
	case Red:
		return "red"
	case Blue:
		return "blue"
	default:
		return "unknown"
	}
}

type State int

const (
	Off State = iota
	On
	Blinking
)

func (s State) String() string {
	switch s {
	case Off:
		return "off"
	case On:
		return "on"
	case Blinking:
		return "blinking"
	default:
		return "unknown"
	}
}

const (
	powerDataOut = 0xF1010100
	powerBlinkEn = 0xF1010108
	hdBayDataOut = 0xF1010140
	hdBayBlinkEn = 0xF1010148

	redBit  = uint32(1) << 28
	blueBit = uint32(1) << 29
)

// hdBayBit returns the Data-Out/Blink-Enable bit for a given bay
// index (0-3). Confirmed for bay0-bay3 = bits 13-16.
//
// CAVEAT: this bit map was confirmed for the (presumably) red/primary
// channel. The blue/activity channel's disassembly trace led into
// SATA-controller register space (0xE0000000+) rather than this GPIO
// window for at least one call site, which may mean hd-bay blue is
// actually driven by a SATA activity indicator, not plain GPIO. This
// has NOT been confirmed on real hardware. Treat Plan(HD0..HD3, Blue,
// ...) as best-effort until verified - see SPEC.md, "unresolved".
func hdBayBit(zone Zone) (uint32, error) {
	var idx int
	switch zone {
	case HD0:
		idx = 0
	case HD1:
		idx = 1
	case HD2:
		idx = 2
	case HD3:
		idx = 3
	default:
		return 0, errBadZone(zone)
	}
	return uint32(1) << (13 + idx), nil
}

func colorBit(c Color) (uint32, uint32, error) {
	switch c {
	case Red:
		return redBit, blueBit, nil
	case Blue:
		return blueBit, redBit, nil
	default:
		return 0, 0, errBadColor(c)
	}
}

// Plan computes the register writes driving zone's color LED to
// state would perform, without touching any hardware - the pure half
// of Set, used directly by the CLI's -n/--dry-run and -D/--debug
// (and by Set itself). Mirrors the vendor handlers' own logic
// exactly: the two colors on one zone are mutually exclusive (the
// other color's bit is always cleared first), and "blinking" sets
// the bit in BOTH the Data-Out and Blink-Enable registers - blinking
// is a genuine hardware feature of the SoC's GPIO controller
// (confirmed by EX2's own `led --help` text: "blue
// blinking(controlled by hw)"), not a software timer loop.
func Plan(zone Zone, color Color, state State) ([]regio.Write, error) {
	var dataOut, blinkEn int64
	var ownBit uint32
	var otherBit uint32
	var err error

	if zone == Power {
		dataOut, blinkEn = powerDataOut, powerBlinkEn
		ownBit, otherBit, err = colorBit(color)
	} else {
		dataOut, blinkEn = hdBayDataOut, hdBayBlinkEn
		ownBit, err = hdBayBit(zone)
		// Only one bit per bay is confirmed (see hdBayBit's doc
		// comment); there is no known "other color" bit to clear
		// here, so otherBit stays 0.
	}
	if err != nil {
		return nil, err
	}

	var ws []regio.Write

	// Clear the other color first (power LED only - mutual exclusion
	// confirmed there; no-op elsewhere since otherBit==0).
	if otherBit != 0 {
		ws = append(ws, regio.Write{Addr: dataOut, Clear: otherBit})
		ws = append(ws, regio.Write{Addr: blinkEn, Clear: otherBit})
	}

	switch state {
	case Off:
		ws = append(ws, regio.Write{Addr: dataOut, Clear: ownBit})
		ws = append(ws, regio.Write{Addr: blinkEn, Clear: ownBit})
	case On:
		ws = append(ws, regio.Write{Addr: dataOut, Set: ownBit})
		ws = append(ws, regio.Write{Addr: blinkEn, Clear: ownBit})
	case Blinking:
		ws = append(ws, regio.Write{Addr: dataOut, Set: ownBit})
		ws = append(ws, regio.Write{Addr: blinkEn, Set: ownBit})
	default:
		return nil, errBadState(state)
	}

	return ws, nil
}

// Set drives zone's color LED to state against real (or fake, in
// tests) register-mapped memory.
func Set(mem *regio.Mem, zone Zone, color Color, state State) error {
	ws, err := Plan(zone, color, state)
	if err != nil {
		return err
	}
	return mem.ApplyAll(ws)
}
