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
// parameter to reject it itself.
//
// EX4's power LED mechanism IS this one - CONFIRMED, not just
// inherited from the (wrong, for EX2) "identical to EX2" assumption
// this comment used to repeat: a live strace of the real EX4 `led
// power blue on` showed it never touches /dev/ttyS1, only /dev/mem
// (page 0xf1010000); decompiling the real `led` binary (the LED
// logic lives there, not in libshare.so) confirmed this package's
// own powerDataOut/powerBlinkEn/redBit/blueBit and clear-other/
// set-own logic exactly, independently, across all of blue/red
// on/off/blinking; and `wd-control-tool -b ex4 -l power red on` lit
// the real red LED on real hardware (EX4's own `led -h` text only
// documents blue for power, but the real dispatch code has a full,
// working red branch too - this package was already right to expose
// it). See SPEC.md's "Power LED" for the full decompiled evidence.
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
	//
	// hdBayReg1 is the EXACT SAME physical register pkg/fan/ex2.go
	// calls "reg2" - the two subsystems share it on entirely disjoint
	// bits (fan: bit 0 only; hd-bay LED: bits 20/21/23/25), safely,
	// via regio.Mem's atomic Set/Clear.
	hdBayReg1 = 0xF1018140
	hdBayReg2 = 0xF1018150

	// hdBayBlinkEn: CONFIRMED on real EX2 hardware via a proper
	// before/after diff of a full register range across a known
	// transition (on -> blinking, for both hd0 and hd1 independently -
	// the technique the earlier reg2-oscillation "confirmation" should
	// have used from the start, see Plan's own HD-bay Blinking case).
	// One bit per bay, reusing the EXACT SAME bit positions as
	// hdBayBits' RED column (hd0=1<<21, hd1=1<<20) - a separate
	// register from both hdBayReg1 and hdBayReg2, and NOT touched by
	// plain on/off at all (confirmed 0x00000000 while a bay was
	// steadily lit). hdBayReg1 stays completely unchanged across the
	// on->blinking transition (confirmed even for hd1, which had never
	// been turned on at all, so its hdBayReg1 bit was 0 throughout, yet
	// the vendor's own blinking command still worked) - hdBayReg1 is
	// simply not part of the blink path. hdBayReg2 DOES change during
	// blinking, but only as a side effect of the hardware blink
	// generator actively driving the pin (the oscillation first
	// observed, now understood as a readback, not a control write) -
	// writing hdBayReg2 was the earlier, wrong guess.
	hdBayBlinkEn = 0xF1018148

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
// confirmed mutual exclusion to enforce here (unlike Power), so
// on/off touch the requested color's own bit in hdBayReg1/hdBayReg2
// AND clear it in hdBayBlinkEn - the clear is confirmed necessary on
// real hardware, not defensive: without it, `on`/`off` have no effect
// at all once a bay was left blinking (the hardware blink generator
// keeps driving the pin and overriding both registers as long as
// hdBayBlinkEn's bit stays set). Blinking is confirmed for RED only
// (hd0/hd1) via that same dedicated register, hdBayBlinkEn - separate
// from the on/off register pair - found by properly diffing a full
// register range across a known on->blinking transition (see
// hdBayBlinkEn's doc comment for why this supersedes an earlier,
// wrong reg2-based guess that had no real effect). BLUE's blinking
// frame remains unconfirmed and is reported as an error
// (errHDBayBlinkingUnconfirmed) rather than assumed symmetric with
// red.
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
		// Also clears hdBayBlinkEn's own bit - CONFIRMED NECESSARY on
		// real hardware: without this, `off` after a real `blinking`
		// had no effect at all (the hardware blink generator keeps
		// overriding hdBayReg1/hdBayReg2 as long as hdBayBlinkEn's bit
		// stays set, so plain Clear writes to those two registers are
		// silently ignored). Harmless to clear unconditionally even
		// when never set (Blue, or Red that was never blinking).
		return []regio.Write{
			{Addr: hdBayReg1, Clear: bit},
			{Addr: hdBayReg2, Clear: bit},
			{Addr: hdBayBlinkEn, Clear: bit},
		}, nil
	case On:
		// Same reasoning as Off above - confirmed necessary, not just
		// defensive: real hardware ignored `on` after `blinking` too,
		// for the identical reason.
		return []regio.Write{
			{Addr: hdBayReg1, Set: bit},
			{Addr: hdBayReg2, Set: bit},
			{Addr: hdBayBlinkEn, Clear: bit},
		}, nil
	case Blinking:
		// Confirmed on real EX2 hardware for RED only (hd0 and hd1) -
		// a PROPER capture this time: a full register-range diff
		// between a known `on` state and the vendor's own `led
		// hd0/hd1 red blinking`, not just two reads taken seconds
		// apart during an already-ambiguous state (that earlier,
		// wrong attempt wrote hdBayReg2 and had no actual effect on
		// real hardware - see hdBayBlinkEn's doc comment for the full
		// story and why this is right this time). Two independent
		// transitions (hd0, already on; hd1, never turned on at all)
		// agreed: only hdBayBlinkEn's own bit changed - hdBayReg1
		// stayed untouched in both cases (confirmed irrelevant, not
		// just unchanged-by-coincidence, since hd1's own hdBayReg1 bit
		// was 0 throughout yet blinking still worked), and hdBayReg2's
		// changes were a side effect of the hardware blink generator
		// actually driving the pin, not something this write needs to
		// touch. BLUE's own blinking frame remains unconfirmed (no
		// report either way) and is still reported as an error rather
		// than assumed symmetric with red.
		if color != Red {
			return nil, errHDBayBlinkingUnconfirmed(zone, color)
		}
		return []regio.Write{
			{Addr: hdBayBlinkEn, Set: bit},
		}, nil
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
