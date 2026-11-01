package led

import (
	"strings"
	"sync"
	"testing"

	"github.com/symops/wd-control-tool/test/fakeregio"
)

func mustRead(t *testing.T, mem interface {
	ReadReg32(int64) (uint32, error)
}, addr int64) uint32 {
	t.Helper()
	v, err := mem.ReadReg32(addr)
	if err != nil {
		t.Fatalf("ReadReg32(0x%x): %v", addr, err)
	}
	return v
}

func TestSet_PowerLED(t *testing.T) {
	mem := fakeregio.New(t)

	if err := Set(mem, Power, Blue, On); err != nil {
		t.Fatalf("Set blue on: %v", err)
	}
	if got := mustRead(t, mem, powerDataOut); got != blueBit {
		t.Errorf("after blue on: DataOut = 0x%x, want 0x%x", got, blueBit)
	}
	if got := mustRead(t, mem, powerBlinkEn); got != 0 {
		t.Errorf("after blue on: BlinkEn = 0x%x, want 0", got)
	}

	if err := Set(mem, Power, Blue, Blinking); err != nil {
		t.Fatalf("Set blue blinking: %v", err)
	}
	if got := mustRead(t, mem, powerDataOut); got != blueBit {
		t.Errorf("after blue blinking: DataOut = 0x%x, want 0x%x", got, blueBit)
	}
	if got := mustRead(t, mem, powerBlinkEn); got != blueBit {
		t.Errorf("after blue blinking: BlinkEn = 0x%x, want 0x%x", got, blueBit)
	}

	// Switching to red must clear blue's bits in both registers - the
	// two colors are mutually exclusive on this one bi-color LED.
	if err := Set(mem, Power, Red, On); err != nil {
		t.Fatalf("Set red on: %v", err)
	}
	if got := mustRead(t, mem, powerDataOut); got != redBit {
		t.Errorf("after red on: DataOut = 0x%x, want 0x%x (blue should be cleared)", got, redBit)
	}
	if got := mustRead(t, mem, powerBlinkEn); got != 0 {
		t.Errorf("after red on: BlinkEn = 0x%x, want 0 (blue's blink bit should be cleared too)", got)
	}

	if err := Set(mem, Power, Red, Off); err != nil {
		t.Fatalf("Set red off: %v", err)
	}
	if got := mustRead(t, mem, powerDataOut); got != 0 {
		t.Errorf("after red off: DataOut = 0x%x, want 0", got)
	}
	if got := mustRead(t, mem, powerBlinkEn); got != 0 {
		t.Errorf("after red off: BlinkEn = 0x%x, want 0", got)
	}
}

// HD-bay zones have no known "other color" bit to clear (unlike
// Power - see hdBayBits' doc comment: red/blue use entirely
// different, non-overlapping bits, and no mutual exclusion has ever
// been observed on real hardware) - turning one bay/color on must
// NOT affect a different bay or the other color.
func TestSet_HDBayLED_ZonesAndColorsAreIndependent(t *testing.T) {
	mem := fakeregio.New(t)

	if err := Set(mem, HD0, Red, On); err != nil {
		t.Fatalf("Set HD0 red on: %v", err)
	}
	if err := Set(mem, HD1, Blue, On); err != nil {
		t.Fatalf("Set HD1 blue on: %v", err)
	}

	want := uint32(1<<21 | 1<<23) // HD0 red + HD1 blue
	got := mustRead(t, mem, hdBayReg1)
	if got != want {
		t.Errorf("hdBayReg1 = 0x%x, want 0x%x (HD0 red and HD1 blue both set, nothing else)", got, want)
	}
	got2 := mustRead(t, mem, hdBayReg2)
	if got2 != want {
		t.Errorf("hdBayReg2 = 0x%x, want 0x%x (both registers always mirror each other)", got2, want)
	}

	// Turning HD0 red off must not touch HD1's blue bit.
	if err := Set(mem, HD0, Red, Off); err != nil {
		t.Fatalf("Set HD0 red off: %v", err)
	}
	got = mustRead(t, mem, hdBayReg1)
	if got != 1<<23 {
		t.Errorf("after HD0 red off: hdBayReg1 = 0x%x, want 0x%x (only HD1 blue left set)", got, uint32(1<<23))
	}
}

// Confirmed on real EX2 hardware: hdBayReg1 and hdBayReg2 ALWAYS
// change by the identical bit together - this is not a Data-Out/
// Blink-Enable split the way Power's two registers are. See
// hdBayReg1's own doc comment for the register-page capture this
// came from.
func TestSet_HDBayLED(t *testing.T) {
	mem := fakeregio.New(t)

	cases := []struct {
		zone  Zone
		color Color
		bit   uint32
	}{
		{HD0, Red, 1 << 21},
		{HD0, Blue, 1 << 25},
		{HD1, Red, 1 << 20},
		{HD1, Blue, 1 << 23},
	}
	for _, c := range cases {
		if err := Set(mem, c.zone, c.color, On); err != nil {
			t.Fatalf("Set %v %v on: %v", c.zone, c.color, err)
		}
		if got := mustRead(t, mem, hdBayReg1); got&c.bit == 0 {
			t.Errorf("%v %v on: hdBayReg1 = 0x%x, want bit 0x%x set", c.zone, c.color, got, c.bit)
		}
		if got := mustRead(t, mem, hdBayReg2); got&c.bit == 0 {
			t.Errorf("%v %v on: hdBayReg2 = 0x%x, want bit 0x%x set", c.zone, c.color, got, c.bit)
		}

		if err := Set(mem, c.zone, c.color, Off); err != nil {
			t.Fatalf("Set %v %v off: %v", c.zone, c.color, err)
		}
		if got := mustRead(t, mem, hdBayReg1); got&c.bit != 0 {
			t.Errorf("%v %v off: hdBayReg1 = 0x%x, want bit 0x%x clear", c.zone, c.color, got, c.bit)
		}
		if got := mustRead(t, mem, hdBayReg2); got&c.bit != 0 {
			t.Errorf("%v %v off: hdBayReg2 = 0x%x, want bit 0x%x clear", c.zone, c.color, got, c.bit)
		}
	}
}

// No real capture has ever shown a "blinking" transition on HD-bay -
// reported as an error rather than guessed (see Plan's doc comment).
func TestPlan_HDBayBlinkingUnconfirmed(t *testing.T) {
	for _, zone := range []Zone{HD0, HD1} {
		for _, color := range []Color{Red, Blue} {
			if _, err := Plan(zone, color, Blinking); err == nil {
				t.Errorf("Plan(%v, %v, Blinking): want error, got nil", zone, color)
			}
		}
	}
}

// HD2/HD3 are EX4-only (EX2 only has hd0/hd1) and have no confirmed
// register bit - the old "1<<(13+idx)" formula this replaced was
// proven wrong for hd0/hd1, so it's not trusted for hd2/hd3 either.
// Must fail loudly, not silently poke a guessed address.
func TestPlan_HD2HD3Unconfirmed(t *testing.T) {
	for _, zone := range []Zone{HD2, HD3} {
		for _, color := range []Color{Red, Blue} {
			for _, state := range []State{On, Off} {
				if _, err := Plan(zone, color, state); err == nil {
					t.Errorf("Plan(%v, %v, %v): want error, got nil", zone, color, state)
				}
			}
		}
	}
}

func TestSet_InvalidZoneOnEX2(t *testing.T) {
	mem := fakeregio.New(t)
	// HD2/HD3 don't physically exist on EX2 and (now) have no
	// confirmed register bit either - Set must report that error,
	// not silently poke a guessed address.
	if err := Set(mem, HD3, Red, On); err == nil {
		t.Fatal("Set(HD3, ...): want error (unconfirmed), got nil")
	}
}

// Plan is the pure half Set uses internally, and what the CLI's
// -n/--dry-run and -D/--debug call directly (so they never touch
// /dev/mem at all). Pin its output against the same expectations
// TestSet_PowerLED/TestSet_HDBayLED already check end-to-end, and
// confirm Set(mem, ...) == mem.ApplyAll(Plan(...)) by construction.
func TestPlan_MatchesSetBehavior(t *testing.T) {
	type call struct {
		zone  Zone
		color Color
		state State
	}
	calls := []call{
		{Power, Blue, On},
		{Power, Blue, Blinking},
		{Power, Red, On},
		{Power, Red, Off},
		{HD0, Red, On},
		{HD1, Blue, On},
	}

	for _, c := range calls {
		planMem := fakeregio.New(t)
		ws, err := Plan(c.zone, c.color, c.state)
		if err != nil {
			t.Fatalf("Plan(%v,%v,%v): %v", c.zone, c.color, c.state, err)
		}
		if err := planMem.ApplyAll(ws); err != nil {
			t.Fatalf("ApplyAll(Plan(%v,%v,%v)): %v", c.zone, c.color, c.state, err)
		}

		setMem := fakeregio.New(t)
		if err := Set(setMem, c.zone, c.color, c.state); err != nil {
			t.Fatalf("Set(%v,%v,%v): %v", c.zone, c.color, c.state, err)
		}

		for _, addr := range []int64{powerDataOut, powerBlinkEn, hdBayReg1, hdBayReg2} {
			a := mustRead(t, planMem, addr)
			b := mustRead(t, setMem, addr)
			if a != b {
				t.Errorf("%v,%v,%v: Plan+ApplyAll(0x%x)=0x%x, Set(0x%x)=0x%x differ", c.zone, c.color, c.state, addr, a, addr, b)
			}
		}
	}
}

func TestPlan_RejectsBadInput(t *testing.T) {
	if _, err := Plan(Power, Color(99), On); err == nil {
		t.Error("Plan with invalid color: want error, got nil")
	} else if !strings.Contains(err.Error(), "99") {
		// The error must name the actual bad value, not "unknown" -
		// Color implements String(), which makes a naive %v on an
		// out-of-range value print exactly "unknown" (verified by
		// actually running this before fixing it, not just reasoning
		// about fmt's behavior).
		t.Errorf("error = %q, want it to mention the value 99", err)
	}
	if _, err := Plan(HD0, Red, State(99)); err == nil {
		t.Error("Plan with invalid state: want error, got nil")
	} else if !strings.Contains(err.Error(), "99") {
		t.Errorf("error = %q, want it to mention the value 99", err)
	}
	// Power's own invalid-state branch is separate code from HD-bay's
	// (found missing via go tool cover: the HD-bay case above never
	// exercised it) - Plan builds Power's otherBit-clearing writes
	// before reaching the state switch, so this also confirms that
	// doesn't mask the error.
	if _, err := Plan(Power, Red, State(99)); err == nil {
		t.Error("Plan(Power, Red, invalid state): want error, got nil")
	} else if !strings.Contains(err.Error(), "99") {
		t.Errorf("error = %q, want it to mention the value 99", err)
	}
	if _, err := Plan(Zone(99), Red, On); err == nil {
		t.Error("Plan with invalid zone: want error, got nil")
	} else if !strings.Contains(err.Error(), "99") {
		t.Errorf("error = %q, want it to mention the value 99", err)
	}
	if _, err := Plan(HD0, Color(99), On); err == nil {
		t.Error("Plan(HD0, invalid color, On): want error, got nil")
	}
}

func TestZoneColorStateStringers(t *testing.T) {
	// Exercised by the CLI's -D/--debug output - a stringer that
	// silently falls back to "unknown" for a valid value would be a
	// confusing regression, so pin the known-good set explicitly.
	zones := map[Zone]string{Power: "power", HD0: "hd0", HD1: "hd1", HD2: "hd2", HD3: "hd3"}
	for z, want := range zones {
		if got := z.String(); got != want {
			t.Errorf("Zone(%d).String() = %q, want %q", z, got, want)
		}
	}
	colors := map[Color]string{Red: "red", Blue: "blue"}
	for c, want := range colors {
		if got := c.String(); got != want {
			t.Errorf("Color(%d).String() = %q, want %q", c, got, want)
		}
	}
	states := map[State]string{Off: "off", On: "on", Blinking: "blinking"}
	for s, want := range states {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

// The "unknown" fallback itself (every Stringer's default case) had
// no coverage - only valid values were ever passed to String() above.
// This is also the exact reason errBadZone/errBadColor/errBadState
// had to stop using %v (see pkg/led/errors.go and
// TestPlan_RejectsBadInput) - make sure the fallback they'd otherwise
// have produced actually is "unknown", not something else.
func TestZoneColorStateStringers_UnknownFallback(t *testing.T) {
	if got := Zone(99).String(); got != "unknown" {
		t.Errorf("Zone(99).String() = %q, want %q", got, "unknown")
	}
	if got := Color(99).String(); got != "unknown" {
		t.Errorf("Color(99).String() = %q, want %q", got, "unknown")
	}
	if got := State(99).String(); got != "unknown" {
		t.Errorf("State(99).String() = %q, want %q", got, "unknown")
	}
}

// Set's own early-return-on-Plan-error branch had no coverage - every
// other Set() call in this file used valid arguments (invalid-input
// coverage so far only went through Plan directly, in
// TestPlan_RejectsBadInput).
func TestSet_RejectsBadInput(t *testing.T) {
	mem := fakeregio.New(t)
	if err := Set(mem, Power, Color(99), On); err == nil {
		t.Error("Set with invalid color: want error, got nil")
	}
}

// Zone/Color/State are plain int-based types, so Plan can be called
// with any int value cast to them, not just the declared constants -
// exactly the kind of out-of-domain input Go's type system doesn't
// stop at compile time. Plan must never panic for any combination,
// and every register address it ever writes to must be one of the 4
// known LED registers (not, say, an out-of-bounds slice access or an
// address computed from an unvalidated index - see hdBayBit's own
// bounds check). Run with
// `go test -fuzz=FuzzPlan_NeverPanicsAndOnlyWritesKnownRegisters ./pkg/led`
// to fuzz beyond the seed corpus.
func FuzzPlan_NeverPanicsAndOnlyWritesKnownRegisters(f *testing.F) {
	f.Add(int(Power), int(Red), int(On))
	f.Add(int(HD1), int(Blue), int(On))
	f.Add(99, 99, 99)
	f.Add(-1, -1, -1)

	knownAddrs := map[int64]bool{
		powerDataOut: true, powerBlinkEn: true,
		hdBayReg1: true, hdBayReg2: true,
	}

	f.Fuzz(func(t *testing.T, zone, color, state int) {
		ws, err := Plan(Zone(zone), Color(color), State(state))
		if err != nil {
			return
		}
		for _, w := range ws {
			if !knownAddrs[w.Addr] {
				t.Fatalf("Plan(%d,%d,%d) wrote to unknown address 0x%x", zone, color, state, w.Addr)
			}
		}
	})
}

// Exercises regio.Mem.SetClearBits' atomicity fix (see that package's
// own TestMem_SetClearBits_ConcurrentDistinctBitsAllSurvive) through
// the actual led.Set API, not just raw regio calls - confirming the
// fix's benefit is real at the level callers (this project's own
// CLI, or any future caller sharing one Mem across goroutines) would
// actually see it. HD0 and HD1's red bits are distinct (see
// hdBayBits), so N goroutines each setting ONE distinct bay on via
// Set concurrently must all survive, with no lost update.
func TestSet_ConcurrentDistinctHDBaysAllSurvive(t *testing.T) {
	mem := fakeregio.New(t)
	cases := []struct {
		zone  Zone
		color Color
	}{
		{HD0, Red}, {HD0, Blue}, {HD1, Red}, {HD1, Blue},
	}

	var wg sync.WaitGroup
	wg.Add(len(cases))
	for _, c := range cases {
		go func(zone Zone, color Color) {
			defer wg.Done()
			if err := Set(mem, zone, color, On); err != nil {
				t.Errorf("Set(%v, %v, On): %v", zone, color, err)
			}
		}(c.zone, c.color)
	}
	wg.Wait()

	want := uint32(1<<21 | 1<<25 | 1<<20 | 1<<23) // HD0 red+blue, HD1 red+blue
	got := mustRead(t, mem, hdBayReg1)
	if got != want {
		t.Errorf("after 4 concurrent Set calls: hdBayReg1 = 0x%x, want 0x%x (missing bits: 0x%x)", got, want, want&^got)
	}
}
