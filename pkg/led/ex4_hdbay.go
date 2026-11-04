package led

import "github.com/symops/wd-control-tool/pkg/regio"

// EX4's HD-bay LED mechanism is NOT the same register pair as EX2's
// hdBayReg1/hdBayReg2 (0xF1018140/0xF1018150, see led.go) - a
// real-hardware report showed `wd-control-tool -b ex4 -l hd0/hd1 red
// on` had no effect at all despite running those same, EX2-confirmed
// writes, and a separate report showed EX4's blue hd-bay LEDs
// toggling ALL FOUR bays together instead of independently, something
// EX2's per-bay-bit model can't produce. Decompiling the real EX4
// `led` binary (the same one SPEC.md's "Power LED" section already
// confirmed for the power LED) found the real, EX4-specific
// mechanism, and it is genuinely two DIFFERENT sub-mechanisms sharing
// one dispatcher function (FUN_0000acb8):
//
//   - Red (FUN_00009c9c, via FUN_00009c00's read-modify-write
//     helper): a per-bay bit in TWO registers, 0xf1010140 and
//     0xf1010148 - the same base PAGE (0xf1010000) EX4's power LED
//     uses (SPEC.md's "Power LED"), just a different pair of
//     registers within it. hd0..hd3 use bits 13..16
//     (0x2000/0x4000/0x8000/0x10000) - unlike EX2's hd-bay bits (no
//     simple formula at all), these DO follow "bit 13+N", confirmed
//     directly from the decompiled constants, not assumed from the
//     old, EX2-disproven formula. "on" sets the bit in reg1 and
//     clears it in reg2; "off" clears it in both; "blinking" sets the
//     bit in reg2 ONLY, leaving reg1 untouched (matching the real
//     decompiled FUN_00009c9c exactly, including this asymmetry with
//     "on"/"off").
//
//   - Blue (FUN_0000ab20): a SINGLE shared register, 0xe00104f0 (page
//     0xe0010000 - an entirely different physical page from red's,
//     also confirmed independently by a live strace that showed
//     exactly this page mmapped for every `led hdN blue on/off`
//     capture). The real vendor code takes a bay-index argument and
//     then ignores it completely - "on" always writes the literal
//     value 0x7c and "off" always writes 0xf44, for ANY of hd0..hd3.
//     This is exactly why real EX4 hardware shows all four bays' blue
//     LEDs switch together: the vendor's own code has no per-bay
//     control for this color at all, not a driver limitation on this
//     tool's side. "blinking" has no branch whatsoever in the real
//     decompiled code (falls through as a pure no-op) - reported here
//     as an error rather than silently doing nothing.
//
// See SPEC.md's "HD-bay" for the full decompiled evidence (pseudocode
// snippets and the live strace page captures this is built from).
const (
	ex4HDBayReg1 = 0xf1010140 // red only - FUN_00009c9c's "reg1" (DataOut-like)
	ex4HDBayReg2 = 0xf1010148 // red only - FUN_00009c9c's "reg2" (BlinkEnable-like)

	ex4HDBayBlueReg      = 0xe00104f0 // blue only - shared by ALL FOUR bays, confirmed
	ex4HDBayBlueOnValue  = uint32(0x7c)
	ex4HDBayBlueOffValue = uint32(0xf44)
)

// ex4HDBayRedBit: hd0..hd3 -> bit 13..16, confirmed by decompiling
// FUN_00009c9c's own hardcoded per-bay constants (not the old,
// EX2-disproven "13+N" guess - this is EX4's real value, independently
// confirmed, that merely happens to match that formula's shape).
func ex4HDBayRedBit(zone Zone) (uint32, error) {
	switch zone {
	case HD0:
		return 1 << 13, nil
	case HD1:
		return 1 << 14, nil
	case HD2:
		return 1 << 15, nil
	case HD3:
		return 1 << 16, nil
	default:
		return 0, errBadZone(zone)
	}
}

// ex4PlainWrite expresses a plain, unconditional register overwrite
// (what the vendor's LIB_Write_Mem does for EX4's blue hd-bay LED - no
// read-modify-write, just store v) as a regio.Write: SetClearBits
// computes (v0|set)&^clear, and with set=v, clear=^v that always
// reduces to exactly v regardless of the register's starting value v0.
func ex4PlainWrite(addr int64, v uint32) []regio.Write {
	return []regio.Write{{Addr: addr, Set: v, Clear: ^v}}
}

// EX4HDBayPlan computes the register writes for an EX4 HD-bay zone
// (hd0..hd3) - the pure half, used directly by -n/--dry-run and
// -D/--debug (and by the CLI's real run), same role as Plan plays for
// EX2's HD-bay and both boards' Power zone. Zone must be HD0..HD3 -
// Power goes through Plan instead (EX4's power LED uses the same
// mechanism as Plan already models - see SPEC.md's "Power LED").
func EX4HDBayPlan(zone Zone, color Color, state State) ([]regio.Write, error) {
	switch color {
	case Blue:
		switch state {
		case Off:
			return ex4PlainWrite(ex4HDBayBlueReg, ex4HDBayBlueOffValue), nil
		case On:
			return ex4PlainWrite(ex4HDBayBlueReg, ex4HDBayBlueOnValue), nil
		case Blinking:
			return nil, errHDBayBlueBlinkingNotImplementedEX4()
		default:
			return nil, errBadState(state)
		}
	case Red:
		bit, err := ex4HDBayRedBit(zone)
		if err != nil {
			return nil, err
		}
		switch state {
		case Off:
			return []regio.Write{
				{Addr: ex4HDBayReg1, Clear: bit},
				{Addr: ex4HDBayReg2, Clear: bit},
			}, nil
		case On:
			return []regio.Write{
				{Addr: ex4HDBayReg1, Set: bit},
				{Addr: ex4HDBayReg2, Clear: bit},
			}, nil
		case Blinking:
			return []regio.Write{
				{Addr: ex4HDBayReg2, Set: bit},
			}, nil
		default:
			return nil, errBadState(state)
		}
	default:
		return nil, errBadColor(color)
	}
}

// EX4HDBaySet drives an EX4 HD-bay zone's color LED to state against
// real (or fake, in tests) register-mapped memory.
func EX4HDBaySet(mem *regio.Mem, zone Zone, color Color, state State) error {
	ws, err := EX4HDBayPlan(zone, color, state)
	if err != nil {
		return err
	}
	return mem.ApplyAll(ws)
}
