package mcu

import (
	"bytes"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/test/fakemcu"
)

// These tests exercise the real transmit path (Port.sendBytes, the
// actual per-byte pacing) over a real pty pair, not just the pure
// frame-building functions already covered by legacy_test.go/
// ex2u_test.go. If the pacing or the write loop itself ever breaks,
// golden-byte tests on the frame bytes alone would not catch it; this
// would.

func TestEX2U_FanPercent_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2U(OpenFile(fake.Slave))

	done := make(chan error, 1)
	go func() { done <- m.FanPercent(50) }()

	// 7 bytes * 100ms pacing = ~700ms; give it real headroom.
	got := fake.ReadFrame(t, 7, 3*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("FanPercent(50): %v", err)
	}

	want := []byte{0xFA, 0x02, 0x00, 0x80, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX2U_LEDBreathe_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2U(OpenFile(fake.Slave))

	done := make(chan error, 1)
	go func() { done <- m.LEDBreathe() }()

	got := fake.ReadFrame(t, 7, 3*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("LEDBreathe: %v", err)
	}

	want := []byte{0xFA, 0x03, 0x06, 0x04, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX4_FanLevel_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))

	done := make(chan error, 1)
	go func() { done <- m.FanLevel(3) }()

	// EX4's inter-byte pacing (10us) is negligible - no need for a
	// generous timeout the way EX2U's 100ms pacing needs one.
	got := fake.ReadFrame(t, 7, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("FanLevel(3): %v", err)
	}

	want := []byte{0xFA, 0x02, 0x00, 0x03, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX4_Display_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))

	text := "Booting..."
	done := make(chan error, 1)
	go func() { done <- m.Display(text) }()

	got := fake.ReadFrame(t, 6+len(text), time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Display(%q): %v", text, err)
	}

	want := append([]byte{0xFA, 0x04, 0x01, 0x00, byte(len(text))}, text...)
	want = append(want, 0xFB)
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX2_FanFull_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2(OpenFile(fake.Slave))

	done := make(chan error, 1)
	go func() { done <- m.FanFull() }()

	got := fake.ReadFrame(t, 7, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("FanFull: %v", err)
	}

	want := []byte{0xFA, 0x02, 0x00, 0x02, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}
