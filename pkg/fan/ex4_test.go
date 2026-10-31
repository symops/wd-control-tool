package fan

import (
	"bytes"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/pkg/mcu"
	"github.com/symops/wd-control-tool/test/fakemcu"
)

func TestEX4_Set_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	f := EX4{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}

	done := make(chan error, 1)
	go func() { done <- f.Set(2) }()

	got := fake.ReadFrame(t, 7, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Set(2): %v", err)
	}

	want := []byte{0xFA, 0x02, 0x00, 0x02, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX4_Set_RejectsOutOfRange(t *testing.T) {
	fake := fakemcu.New(t)
	f := EX4{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}
	for _, bad := range []int{-1, 5, 100} {
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%d): want error, got nil", bad)
		}
	}
}

func TestEX4_Set_AllLevels(t *testing.T) {
	for level := 0; level <= 4; level++ {
		fake := fakemcu.New(t)
		f := EX4{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}

		done := make(chan error, 1)
		go func() { done <- f.Set(level) }()

		got := fake.ReadFrame(t, 7, time.Second)
		if err := <-done; err != nil {
			t.Fatalf("Set(%d): %v", level, err)
		}

		want := []byte{0xFA, 0x02, 0x00, byte(level), 0x00, 0x00, 0xFB}
		if !bytes.Equal(got, want) {
			t.Errorf("level %d: wire bytes = % X, want % X", level, got, want)
		}
	}
}
