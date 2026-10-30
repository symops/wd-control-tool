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
