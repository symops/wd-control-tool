package fan

import (
	"testing"

	"github.com/symops/wd-control-tool/test/fakeregio"
)

func TestEX2_Set(t *testing.T) {
	mem := fakeregio.New(t)
	f := EX2{Mem: mem}

	readReg1 := func(t *testing.T) uint32 {
		v, err := mem.ReadReg32(ex2FanReg1)
		if err != nil {
			t.Fatalf("ReadReg32(reg1): %v", err)
		}
		return v
	}
	readReg2 := func(t *testing.T) uint32 {
		v, err := mem.ReadReg32(ex2FanReg2)
		if err != nil {
			t.Fatalf("ReadReg32(reg2): %v", err)
		}
		return v
	}

	// Level 2 -> ex2FanBits[1] = {reg1, 0x1000} - confirmed on real
	// EX2 hardware via fan_control's own RPM read-back (5357 RPM),
	// not the jump-table-order guess this used to be. See ex2.go.
	if err := f.Set(2); err != nil {
		t.Fatalf("Set(2): %v", err)
	}
	if got := readReg1(t); got != 0x1000 {
		t.Errorf("after Set(2): reg1 = 0x%x, want 0x1000", got)
	}
	if got := readReg2(t); got != 0 {
		t.Errorf("after Set(2): reg2 = 0x%x, want 0", got)
	}

	// Level 5 -> ex2FanBits[4] = {reg2, 0x1} (confirmed 8333 RPM).
	// Switching levels must clear the PREVIOUS level's bit too
	// (Clear_All_Fan_Register is called every time, not just on
	// Set(0)).
	if err := f.Set(5); err != nil {
		t.Fatalf("Set(5): %v", err)
	}
	if got := readReg1(t); got != 0 {
		t.Errorf("after Set(5): reg1 = 0x%x, want 0 (previous level's bit should be cleared)", got)
	}
	if got := readReg2(t); got != 0x1 {
		t.Errorf("after Set(5): reg2 = 0x%x, want 0x1", got)
	}

	// Level 0 (Stop) clears everything.
	if err := f.Set(0); err != nil {
		t.Fatalf("Set(0): %v", err)
	}
	if got := readReg1(t); got != 0 {
		t.Errorf("after Set(0): reg1 = 0x%x, want 0", got)
	}
	if got := readReg2(t); got != 0 {
		t.Errorf("after Set(0): reg2 = 0x%x, want 0", got)
	}
}

func TestEX2_Set_RejectsOutOfRange(t *testing.T) {
	mem := fakeregio.New(t)
	f := EX2{Mem: mem}
	for _, bad := range []int{-1, 7, 100} {
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%d): want error, got nil", bad)
		}
	}
}

// Ex2FanPlan is the pure half Set uses internally, and what the
// CLI's -n/--dry-run and -D/--debug call directly (so they never
// open /dev/mem at all). Confirm every level's plan, applied by hand,
// produces exactly what Set itself produces.
func TestEx2FanPlan_MatchesSetForEveryLevel(t *testing.T) {
	for level := 0; level <= 6; level++ {
		planMem := fakeregio.New(t)
		ws, err := Ex2FanPlan(level)
		if err != nil {
			t.Fatalf("Ex2FanPlan(%d): %v", level, err)
		}
		if err := planMem.ApplyAll(ws); err != nil {
			t.Fatalf("ApplyAll(Ex2FanPlan(%d)): %v", level, err)
		}

		setMem := fakeregio.New(t)
		if err := (EX2{Mem: setMem}).Set(level); err != nil {
			t.Fatalf("Set(%d): %v", level, err)
		}

		for _, addr := range []int64{ex2FanReg1, ex2FanReg2} {
			a, _ := planMem.ReadReg32(addr)
			b, _ := setMem.ReadReg32(addr)
			if a != b {
				t.Errorf("level %d: Plan+ApplyAll(0x%x)=0x%x, Set(0x%x)=0x%x differ", level, addr, a, addr, b)
			}
		}
	}
}

func TestEx2FanPlan_RejectsOutOfRange(t *testing.T) {
	for _, bad := range []int{-1, 7, 100} {
		if _, err := Ex2FanPlan(bad); err == nil {
			t.Errorf("Ex2FanPlan(%d): want error, got nil", bad)
		}
	}
}

func TestEx2FanPlan_AlwaysClearsFirst(t *testing.T) {
	// Every level, including 0, must start with the two
	// Clear_All_Fan_Register writes - confirmed by checking the first
	// two entries of every plan, not just level 0's.
	for level := 0; level <= 6; level++ {
		ws, err := Ex2FanPlan(level)
		if err != nil {
			t.Fatalf("Ex2FanPlan(%d): %v", level, err)
		}
		if len(ws) < 2 {
			t.Fatalf("Ex2FanPlan(%d): only %d writes, want at least 2", level, len(ws))
		}
		if ws[0].Addr != ex2FanReg1 || ws[0].Clear != ex2FanClearMask1 {
			t.Errorf("Ex2FanPlan(%d)[0] = %+v, want clear of reg1's mask", level, ws[0])
		}
		if ws[1].Addr != ex2FanReg2 || ws[1].Clear != ex2FanClearMask2 {
			t.Errorf("Ex2FanPlan(%d)[1] = %+v, want clear of reg2's mask", level, ws[1])
		}
	}
}

// Ex2FanPlan must never panic for any int, and every write it
// produces must target one of the 2 known fan registers - the level
// range check (0-6) is the only thing standing between an arbitrary
// caller-supplied int and ex2FanBits[level-1]'s array index. Run with
// `go test -fuzz=FuzzEx2FanPlan_NeverPanicsAndOnlyWritesKnownRegisters ./pkg/fan`
// to fuzz beyond the seed corpus.
func FuzzEx2FanPlan_NeverPanicsAndOnlyWritesKnownRegisters(f *testing.F) {
	f.Add(0)
	f.Add(6)
	f.Add(-1)
	f.Add(7)
	f.Add(1 << 20)

	knownAddrs := map[int64]bool{ex2FanReg1: true, ex2FanReg2: true}

	f.Fuzz(func(t *testing.T, level int) {
		ws, err := Ex2FanPlan(level)
		if err != nil {
			return
		}
		for _, w := range ws {
			if !knownAddrs[w.Addr] {
				t.Fatalf("Ex2FanPlan(%d) wrote to unknown address 0x%x", level, w.Addr)
			}
		}
	})
}
