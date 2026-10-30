package fan

import (
	"bytes"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/pkg/mcu"
	"github.com/symops/wd-control-tool/test/fakemcu"
)

func TestEX2U_Set_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	f := EX2U{MCU: mcu.NewEX2U(mcu.OpenFile(fake.Slave))}

	done := make(chan error, 1)
	go func() { done <- f.Set(100) }()

	got := fake.ReadFrame(t, 7, 3*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Set(100): %v", err)
	}

	want := []byte{0xFA, 0x02, 0x00, 0xFF, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX2U_Set_RejectsUnsupported(t *testing.T) {
	fake := fakemcu.New(t)
	f := EX2U{MCU: mcu.NewEX2U(mcu.OpenFile(fake.Slave))}
	if err := f.Set(42); err == nil {
		t.Error("Set(42): want error, got nil")
	}
}
