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

	// Level 2 -> ex2FanBits[1] = {reg1, 0x100} per the current
	// (inferred, see SPEC.md) ordering.
	if err := f.Set(2); err != nil {
		t.Fatalf("Set(2): %v", err)
	}
	if got := readReg1(t); got != 0x100 {
		t.Errorf("after Set(2): reg1 = 0x%x, want 0x100", got)
	}
	if got := readReg2(t); got != 0 {
		t.Errorf("after Set(2): reg2 = 0x%x, want 0", got)
	}

	// Level 6 -> ex2FanBits[5] = {reg2, 0x1}. Switching levels must
	// clear the PREVIOUS level's bit too (Clear_All_Fan_Register is
	// called every time, not just on Set(0)).
	if err := f.Set(6); err != nil {
		t.Fatalf("Set(6): %v", err)
	}
	if got := readReg1(t); got != 0 {
		t.Errorf("after Set(6): reg1 = 0x%x, want 0 (previous level's bit should be cleared)", got)
	}
	if got := readReg2(t); got != 0x1 {
		t.Errorf("after Set(6): reg2 = 0x%x, want 0x1", got)
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
