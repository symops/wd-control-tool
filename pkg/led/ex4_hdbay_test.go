package led

import (
	"testing"

	"github.com/symops/wd-control-tool/test/fakeregio"
)

func TestEX4HDBayPlan_RedPerBayBits(t *testing.T) {
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
		ws, err := EX4HDBayPlan(c.zone, Red, On)
		if err != nil {
			t.Fatalf("zone=%v red on: %v", c.zone, err)
		}
		want := []struct {
			Addr       int64
			Set, Clear uint32
		}{
			{ex4HDBayReg1, c.bit, 0},
			{ex4HDBayReg2, 0, c.bit},
		}
		if len(ws) != 2 {
			t.Fatalf("zone=%v red on: got %d writes, want 2", c.zone, len(ws))
		}
		for i, w := range ws {
			if w.Addr != want[i].Addr || w.Set != want[i].Set || w.Clear != want[i].Clear {
				t.Errorf("zone=%v red on write[%d] = %+v, want %+v", c.zone, i, w, want[i])
			}
		}
	}
}

func TestEX4HDBayPlan_RedOff_ClearsBothRegisters(t *testing.T) {
	ws, err := EX4HDBayPlan(HD1, Red, Off)
	if err != nil {
		t.Fatalf("red off: %v", err)
	}
	bit := uint32(1 << 14)
	want := []struct {
		Addr  int64
		Clear uint32
	}{
		{ex4HDBayReg1, bit},
		{ex4HDBayReg2, bit},
	}
	if len(ws) != 2 {
		t.Fatalf("got %d writes, want 2", len(ws))
	}
	for i, w := range ws {
		if w.Addr != want[i].Addr || w.Set != 0 || w.Clear != want[i].Clear {
			t.Errorf("write[%d] = %+v, want Addr=0x%x Clear=0x%x Set=0", i, w, want[i].Addr, want[i].Clear)
		}
	}
}

// TestEX4HDBayPlan_RedBlinking_OnlyTouchesReg2 pins down the real
// decompiled asymmetry: "blinking" only sets reg2's bit, leaving reg1
// untouched - NOT "set both", unlike on/off's symmetric treatment of
// the two registers. See ex4_hdbay.go's doc comment.
func TestEX4HDBayPlan_RedBlinking_OnlyTouchesReg2(t *testing.T) {
	ws, err := EX4HDBayPlan(HD0, Red, Blinking)
	if err != nil {
		t.Fatalf("red blinking: %v", err)
	}
	if len(ws) != 1 {
		t.Fatalf("got %d writes, want exactly 1 (reg2 only)", len(ws))
	}
	w := ws[0]
	if w.Addr != ex4HDBayReg2 || w.Set != 1<<13 || w.Clear != 0 {
		t.Errorf("write = %+v, want Addr=0x%x Set=0x2000 Clear=0", w, int64(ex4HDBayReg2))
	}
}

// TestEX4HDBayPlan_Blue_IgnoresZone confirms the real-hardware/decompiled
// finding: blue hd-bay writes the SAME shared register with the SAME
// value regardless of which bay zone was requested - this is why real
// EX4 hardware shows all four bays' blue LEDs toggle together, not a
// bug in this tool.
func TestEX4HDBayPlan_Blue_IgnoresZone(t *testing.T) {
	for _, zone := range []Zone{HD0, HD1, HD2, HD3} {
		on, err := EX4HDBayPlan(zone, Blue, On)
		if err != nil {
			t.Fatalf("zone=%v blue on: %v", zone, err)
		}
		if len(on) != 1 || on[0].Addr != ex4HDBayBlueReg || on[0].Set != ex4HDBayBlueOnValue || on[0].Clear != ^ex4HDBayBlueOnValue {
			t.Errorf("zone=%v blue on = %+v, want single write Addr=0x%x Set=0x%x", zone, on, int64(ex4HDBayBlueReg), ex4HDBayBlueOnValue)
		}

		off, err := EX4HDBayPlan(zone, Blue, Off)
		if err != nil {
			t.Fatalf("zone=%v blue off: %v", zone, err)
		}
		if len(off) != 1 || off[0].Addr != ex4HDBayBlueReg || off[0].Set != ex4HDBayBlueOffValue || off[0].Clear != ^ex4HDBayBlueOffValue {
			t.Errorf("zone=%v blue off = %+v, want single write Addr=0x%x Set=0x%x", zone, off, int64(ex4HDBayBlueReg), ex4HDBayBlueOffValue)
		}
	}
}

func TestEX4HDBayPlan_BlueBlinking_NotImplemented(t *testing.T) {
	_, err := EX4HDBayPlan(HD0, Blue, Blinking)
	if err == nil {
		t.Fatal("blue blinking: expected an error, got nil")
	}
}

func TestEX4HDBayPlan_InvalidZone(t *testing.T) {
	if _, err := EX4HDBayPlan(Power, Red, On); err == nil {
		t.Error("zone=Power: expected an error (Power goes through Plan, not EX4HDBayPlan), got nil")
	}
}

func TestEX4HDBayPlan_InvalidColor(t *testing.T) {
	if _, err := EX4HDBayPlan(HD0, Color(99), On); err == nil {
		t.Error("expected an error for an invalid color, got nil")
	}
}

func TestEX4HDBayPlan_InvalidState(t *testing.T) {
	if _, err := EX4HDBayPlan(HD0, Red, State(99)); err == nil {
		t.Error("expected an error for an invalid state, got nil")
	}
	if _, err := EX4HDBayPlan(HD0, Blue, State(99)); err == nil {
		t.Error("expected an error for an invalid state, got nil")
	}
}

// TestEX4HDBaySet_Blue_AllBaysShareOneRegister is the integration-level
// version of TestEX4HDBayPlan_Blue_IgnoresZone, over fakeregio: turning
// on hd0's blue and then reading the SAME register through a
// completely different zone constant proves there is only one physical
// bit of state here, not four.
func TestEX4HDBaySet_Blue_AllBaysShareOneRegister(t *testing.T) {
	mem := fakeregio.New(t)

	if err := EX4HDBaySet(mem, HD0, Blue, On); err != nil {
		t.Fatalf("hd0 blue on: %v", err)
	}
	if got := mustRead(t, mem, ex4HDBayBlueReg); got != ex4HDBayBlueOnValue {
		t.Errorf("after hd0 blue on: reg = 0x%x, want 0x%x", got, ex4HDBayBlueOnValue)
	}

	// hd3's "off" must also affect this exact same register - there is
	// no separate state for hd0 vs hd3 on real hardware.
	if err := EX4HDBaySet(mem, HD3, Blue, Off); err != nil {
		t.Fatalf("hd3 blue off: %v", err)
	}
	if got := mustRead(t, mem, ex4HDBayBlueReg); got != ex4HDBayBlueOffValue {
		t.Errorf("after hd3 blue off: reg = 0x%x, want 0x%x", got, ex4HDBayBlueOffValue)
	}
}

func TestEX4HDBaySet_Red_PerBayIndependence(t *testing.T) {
	mem := fakeregio.New(t)

	if err := EX4HDBaySet(mem, HD0, Red, On); err != nil {
		t.Fatalf("hd0 red on: %v", err)
	}
	if err := EX4HDBaySet(mem, HD1, Red, On); err != nil {
		t.Fatalf("hd1 red on: %v", err)
	}
	got := mustRead(t, mem, ex4HDBayReg1)
	want := uint32(1<<13 | 1<<14)
	if got != want {
		t.Errorf("reg1 after hd0+hd1 red on = 0x%x, want 0x%x (both bits set, independently)", got, want)
	}

	if err := EX4HDBaySet(mem, HD0, Red, Off); err != nil {
		t.Fatalf("hd0 red off: %v", err)
	}
	got = mustRead(t, mem, ex4HDBayReg1)
	if got != 1<<14 {
		t.Errorf("reg1 after hd0 red off = 0x%x, want 0x%x (hd1's bit must survive)", got, uint32(1<<14))
	}
}

// Same reasoning as pkg/led's own FuzzPlan_NeverPanicsAndOnlyWritesKnownRegisters:
// Zone/Color/State are plain int-based types, so EX4HDBayPlan can be
// called with any int value cast to them. Must never panic for any
// combination, and every register address it ever writes to must be
// one of the 3 known EX4 HD-bay registers.
func FuzzEX4HDBayPlan_NeverPanicsAndOnlyWritesKnownRegisters(f *testing.F) {
	f.Add(int(HD0), int(Red), int(On))
	f.Add(int(HD3), int(Blue), int(Blinking))
	f.Add(int(Power), int(Red), int(On)) // not a valid zone for this function - must error, not panic
	f.Add(99, 99, 99)
	f.Add(-1, -1, -1)

	knownAddrs := map[int64]bool{
		ex4HDBayReg1: true, ex4HDBayReg2: true, ex4HDBayBlueReg: true,
	}

	f.Fuzz(func(t *testing.T, zone, color, state int) {
		ws, err := EX4HDBayPlan(Zone(zone), Color(color), State(state))
		if err != nil {
			return
		}
		for _, w := range ws {
			if !knownAddrs[w.Addr] {
				t.Fatalf("EX4HDBayPlan(%d,%d,%d) wrote to unknown address 0x%x", zone, color, state, w.Addr)
			}
		}
	})
}
