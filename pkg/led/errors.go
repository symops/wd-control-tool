package led

import "fmt"

// These report the raw numeric value, not %v - Zone/Color/State all
// implement String(), which makes %v print "unknown" for exactly the
// out-of-range values these helpers exist to report, defeating the
// point of the error message. (Found by actually running one of
// these through fmt.Println - see pkg/led's own tests.)
func errBadZone(z Zone) error   { return fmt.Errorf("led: invalid zone %d", int(z)) }
func errBadColor(c Color) error { return fmt.Errorf("led: invalid color %d", int(c)) }
func errBadState(s State) error { return fmt.Errorf("led: invalid state %d", int(s)) }

// errHDBayZoneUnconfirmed: hd2/hd3 are EX4-only (EX2 only has
// hd0/hd1) and have no confirmed register bit at all - see hdBayBits.
func errHDBayZoneUnconfirmed(z Zone) error {
	return fmt.Errorf("led: no confirmed register bit for HD-bay zone %v (EX2 only has hd0/hd1; hd2/hd3 are EX4-only and unconfirmed on real hardware)", z)
}

// errHDBayBlinkingUnconfirmed: no real capture has ever shown what
// "blinking" does on HD-bay - see Plan's doc comment.
func errHDBayBlinkingUnconfirmed(z Zone, c Color) error {
	return fmt.Errorf("led: HD-bay blinking has no confirmed register behavior (zone=%v color=%v)", z, c)
}

// errHDBayBlueBlinkingNotImplementedEX4: unlike errHDBayBlinkingUnconfirmed
// above (no real capture exists either way), this one IS confirmed -
// decompiling the real EX4 `led` binary's blue hd-bay handler
// (FUN_0000ab20) found no "blinking" branch at all; it's a genuine
// no-op in the vendor's own code, not a gap in this tool. See
// EX4HDBayPlan and SPEC.md's "HD-bay".
func errHDBayBlueBlinkingNotImplementedEX4() error {
	return fmt.Errorf("led: EX4's own vendor code has no HD-bay blue blinking at all (confirmed by decompilation, not just unconfirmed)")
}
