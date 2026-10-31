package led

import (
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

// Unlike the power LED's two colors, HD-bay zones have no known
// "other bit" to clear (see hdBayBit's doc comment) - turning one bay
// on must NOT affect a different bay's bit.
func TestSet_HDBayLED_ZonesAreIndependent(t *testing.T) {
	mem := fakeregio.New(t)

	if err := Set(mem, HD0, Red, On); err != nil {
		t.Fatalf("Set HD0 red on: %v", err)
	}
	if err := Set(mem, HD2, Red, Blinking); err != nil {
		t.Fatalf("Set HD2 red blinking: %v", err)
	}

	got := mustRead(t, mem, hdBayDataOut)
	wantBits := uint32(1<<13 | 1<<15) // HD0's on-bit + HD2's on-bit (blinking also sets DataOut)
	if got != wantBits {
		t.Errorf("DataOut = 0x%x, want 0x%x (HD0 and HD2 both set, HD1/HD3 untouched)", got, wantBits)
	}

	gotBlink := mustRead(t, mem, hdBayBlinkEn)
	if gotBlink != 1<<15 {
		t.Errorf("BlinkEn = 0x%x, want 0x%x (only HD2 blinking)", gotBlink, uint32(1<<15))
	}

	// Turning HD0 off must not touch HD2's bits.
	if err := Set(mem, HD0, Red, Off); err != nil {
		t.Fatalf("Set HD0 red off: %v", err)
	}
	got = mustRead(t, mem, hdBayDataOut)
	if got != 1<<15 {
		t.Errorf("after HD0 off: DataOut = 0x%x, want 0x%x (only HD2 left set)", got, uint32(1<<15))
	}
}

func TestSet_HDBayLED(t *testing.T) {
	mem := fakeregio.New(t)

	cases := []struct {
		zone Zone
		bit  uint32
	}{
		{HD0, 1 << 13},
		{HD1, 1 << 14},
		{HD2, 1 << 15},
		{HD3, 1 << 16},
	}
	for _, c := range cases {
		if err := Set(mem, c.zone, Red, On); err != nil {
			t.Fatalf("Set %v red on: %v", c.zone, err)
		}
		if got := mustRead(t, mem, hdBayDataOut); got&c.bit == 0 {
			t.Errorf("%v on: DataOut = 0x%x, want bit 0x%x set", c.zone, got, c.bit)
		}
		if got := mustRead(t, mem, hdBayBlinkEn); got&c.bit != 0 {
			t.Errorf("%v on: BlinkEn = 0x%x, want bit 0x%x clear", c.zone, got, c.bit)
		}

		if err := Set(mem, c.zone, Red, Blinking); err != nil {
			t.Fatalf("Set %v red blinking: %v", c.zone, err)
		}
		if got := mustRead(t, mem, hdBayBlinkEn); got&c.bit == 0 {
			t.Errorf("%v blinking: BlinkEn = 0x%x, want bit 0x%x set", c.zone, got, c.bit)
		}

		if err := Set(mem, c.zone, Red, Off); err != nil {
			t.Fatalf("Set %v red off: %v", c.zone, err)
		}
		if got := mustRead(t, mem, hdBayDataOut); got&c.bit != 0 {
			t.Errorf("%v off: DataOut = 0x%x, want bit 0x%x clear", c.zone, got, c.bit)
		}
		if got := mustRead(t, mem, hdBayBlinkEn); got&c.bit != 0 {
			t.Errorf("%v off: BlinkEn = 0x%x, want bit 0x%x clear", c.zone, got, c.bit)
		}
	}
}

func TestSet_InvalidZoneOnEX2(t *testing.T) {
	mem := fakeregio.New(t)
	// HD2/HD3 don't physically exist on EX2 - the CLI layer rejects
	// them before calling Set (see cmd/wd-control-tool/main.go), but
	// Set itself doesn't know which board it's on, so this just
	// documents that Set alone will happily poke HD2/HD3's bits; the
	// board-aware rejection lives at the CLI layer, not here.
	if err := Set(mem, HD3, Red, On); err != nil {
		t.Fatalf("Set(HD3, ...) unexpectedly failed: %v", err)
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
		{HD3, Red, Blinking},
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

		for _, addr := range []int64{powerDataOut, powerBlinkEn, hdBayDataOut, hdBayBlinkEn} {
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
	}
	if _, err := Plan(HD0, Red, State(99)); err == nil {
		t.Error("Plan with invalid state: want error, got nil")
	}
	if _, err := Plan(Zone(99), Red, On); err == nil {
		t.Error("Plan with invalid zone: want error, got nil")
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
