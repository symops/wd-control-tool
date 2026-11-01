// Package led implements EX2/EX4's HD-bay LED control (hd0..hd3) and
// EX4's power LED - direct /dev/mem register pokes against a
// Marvell-style SoC GPIO controller at physical base 0xF1000000, NOT
// the MCU/UART channel, NOT sysfs, NOT I2C. See SPEC.md, "LED
// (EX2/EX4)".
//
// EX2's power LED is NOT handled here, despite Plan/Set accepting a
// Power zone that would compute GPIO writes for it: a live hardware
// capture showed EX2's real power LED is MCU-driven (see
// pkg/mcu.EX2PowerLEDFrames), not GPIO - the CLI
// (cmd/wd-control-tool's buildEX2PowerLEDAction) intercepts board
// EX2 + zone Power before ever calling into this package. Plan/Set
// still compute the (now known-wrong-for-EX2) GPIO writes for that
// combination if called directly - this package has no board
// parameter to reject it itself. EX4's power LED mechanism remains
// unconfirmed on real hardware either way (the original "confirmed
// identical to EX2" claim rested on the same untested decompile that
// was wrong for EX2) and is left as GPIO pending an EX4-specific
// capture.
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

	// hdBayReg1/hdBayReg2: CORRECTED from the old hdBayDataOut/
	// hdBayBlinkEn (0xF1010140/0xF1010148) after a real-hardware
	// failure report (hd0/hd1, both colors, had no physical effect
	// through this tool despite the vendor's own `led hd0/hd1 ...
	// on/off` working). A full 0xF1018000-page register scan,
	// before/after every real on/off transition for both bays and
	// both colors on real EX2 hardware, found the truth: these two
	// registers ALWAYS change by the identical bit together - this
	// is NOT a Data-Out/Blink-Enable pair the way Power's two
	// registers are (no register here ever showed a DIFFERENT bit
	// than the other for the same transition). See hdBayBits for the
	// confirmed bits and SPEC.md, "LED (EX2/EX4)", for the full
	// capture this came from.
	hdBayReg1 = 0xF1018140
	hdBayReg2 = 0xF1018150

	redBit  = uint32(1) << 28
	blueBit = uint32(1) << 29
)

// hdBayBits: CONFIRMED on real EX2 hardware (see hdBayReg1's doc
// comment) - deliberately a lookup table, not a formula: the bits
// have no simple stride relating bay index to bit number (hd0
// red=21/blue=25; hd1 red=20/blue=23 - a gap of 4 for hd0, 3 for
// hd1). hd2/hd3 are EX4-only (EX2 only has hd0/hd1 - see SPEC.md) and
// have NO confirmed bits at all: no EX4 hardware has been available
// to test them, and the OLD "1<<(13+idx)" formula this table
// replaces was proven wrong for hd0/hd1, so there is no reason left
// to trust it for hd2/hd3 either - see hdBayBit, which reports that
// plainly as an error rather than guessing.
var hdBayBits = map[Zone]map[Color]uint32{
	HD0: {Red: 1 << 21, Blue: 1 << 25},
	HD1: {Red: 1 << 20, Blue: 1 << 23},
}

// hdBayBit looks up zone/color's confirmed bit (see hdBayBits).
func hdBayBit(zone Zone, color Color) (uint32, error) {
	byColor, ok := hdBayBits[zone]
	if !ok {
		if zone == HD2 || zone == HD3 {
			return 0, errHDBayZoneUnconfirmed(zone)
		}
		return 0, errBadZone(zone)
	}
	bit, ok := byColor[color]
	if !ok {
		return 0, errBadColor(color)
	}
	return bit, nil
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
// (and by Set itself).
//
// Power: mirrors the vendor handlers' own logic - the two colors are
// mutually exclusive (the other color's bit is always cleared
// first), and "blinking" sets the bit in BOTH the Data-Out and
// Blink-Enable registers - a genuine hardware feature of the SoC's
// GPIO controller (confirmed by EX2's own `led --help` text: "blue
// blinking(controlled by hw)"), not a software timer loop.
//
// HD-bay: a real register-page capture showed red/blue use entirely
// DIFFERENT, non-overlapping bits (see hdBayBits) - there is no
// confirmed mutual exclusion to enforce here (unlike Power), so this
// only ever touches the requested color's own bit, in both
// registers. No real on/off transition has ever produced a frame
// matching a "blinking" state for HD-bay, so that's reported as an
// error (errHDBayBlinkingUnconfirmed) rather than guessed.
func Plan(zone Zone, color Color, state State) ([]regio.Write, error) {
	if zone == Power {
		ownBit, otherBit, err := colorBit(color)
		if err != nil {
			return nil, err
		}
		ws := []regio.Write{
			{Addr: powerDataOut, Clear: otherBit},
			{Addr: powerBlinkEn, Clear: otherBit},
		}
		switch state {
		case Off:
			ws = append(ws, regio.Write{Addr: powerDataOut, Clear: ownBit})
			ws = append(ws, regio.Write{Addr: powerBlinkEn, Clear: ownBit})
		case On:
			ws = append(ws, regio.Write{Addr: powerDataOut, Set: ownBit})
			ws = append(ws, regio.Write{Addr: powerBlinkEn, Clear: ownBit})
		case Blinking:
			ws = append(ws, regio.Write{Addr: powerDataOut, Set: ownBit})
			ws = append(ws, regio.Write{Addr: powerBlinkEn, Set: ownBit})
		default:
			return nil, errBadState(state)
		}
		return ws, nil
	}

	bit, err := hdBayBit(zone, color)
	if err != nil {
		return nil, err
	}
	switch state {
	case Off:
		return []regio.Write{
			{Addr: hdBayReg1, Clear: bit},
			{Addr: hdBayReg2, Clear: bit},
		}, nil
	case On:
		return []regio.Write{
			{Addr: hdBayReg1, Set: bit},
			{Addr: hdBayReg2, Set: bit},
		}, nil
	case Blinking:
		return nil, errHDBayBlinkingUnconfirmed(zone, color)
	default:
		return nil, errBadState(state)
	}
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
