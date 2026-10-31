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

func TestEX4_DeviceReady_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))

	done := make(chan error, 1)
	go func() { done <- m.DeviceReady() }()

	got := fake.ReadFrame(t, 7, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("DeviceReady: %v", err)
	}

	want := []byte{0xFA, 0x03, 0x01, 0x00, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX4_RequestDateAndTime_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))

	done := make(chan error, 1)
	go func() { done <- m.RequestDateAndTime() }()

	got := fake.ReadFrame(t, 7, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("RequestDateAndTime: %v", err)
	}

	want := []byte{0xFA, 0x01, 0x00, 0x03, 0x01, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX4_ShutdownFamily_OverPTY(t *testing.T) {
	cases := []struct {
		name string
		call func(m *EX4) error
		want []byte
	}{
		{"DeviceShutdown", func(m *EX4) error { return m.DeviceShutdown(0x01) }, []byte{0xFA, 0x03, 0x03, 0x01, 0x01, 0x01, 0xFB}},
		{"MCUReset", func(m *EX4) error { return m.MCUReset(0x02) }, []byte{0xFA, 0x03, 0x03, 0x02, 0x01, 0x02, 0xFB}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := fakemcu.New(t)
			m := NewEX4(OpenFile(fake.Slave))

			done := make(chan error, 1)
			go func() { done <- c.call(m) }()

			got := fake.ReadFrame(t, len(c.want), time.Second)
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if !bytes.Equal(got, c.want) {
				t.Errorf("%s wire bytes = % X, want % X", c.name, got, c.want)
			}
		})
	}
}

// Found by the same reasoning that caught the toBCD bug: a raw
// caller-supplied byte landing directly in the frame's payload can
// corrupt the frame if it happens to equal FA/FB. DeviceShutdown/
// MCUReset take an undocumented "code" byte with no real validation
// possible, but colliding with the frame markers specifically must
// still be rejected.
//
// This deliberately does NOT also try to confirm zero bytes reached
// the wire via a deadlined Read on fake.Master: in this environment,
// os.File.SetReadDeadline on a /dev/ptmx-derived fd is accepted
// (returns no error) but does not actually unblock a pending Read -
// confirmed by watching it hang well past the deadline under `go
// test -timeout`, not by assumption. That made an earlier version of
// this test itself hang. The error check below is the real guard
// being tested either way; apply() is never reached once an error is
// returned, so there is nothing left to confirm about the wire.
func TestEX4_ShutdownFamily_RejectsFrameMarkerCollision(t *testing.T) {
	for _, value := range []byte{0xFA, 0xFB} {
		fake := fakemcu.New(t)
		m := NewEX4(OpenFile(fake.Slave))

		if err := m.DeviceShutdown(value); err == nil {
			t.Errorf("DeviceShutdown(0x%02X): want error, got nil", value)
		}
		if err := m.MCUReset(value); err == nil {
			t.Errorf("MCUReset(0x%02X): want error, got nil", value)
		}
	}
}

func TestEX2_AllMethods_OverPTY(t *testing.T) {
	cases := []struct {
		name string
		call func(m *EX2) error
		want []byte
	}{
		{"FanStop", (*EX2).FanStop, []byte{0xFA, 0x02, 0x00, 0x00, 0x00, 0x00, 0xFB}},
		{"FanHalf", (*EX2).FanHalf, []byte{0xFA, 0x02, 0x00, 0x01, 0x00, 0x00, 0xFB}},
		{"FanFull", (*EX2).FanFull, []byte{0xFA, 0x02, 0x00, 0x02, 0x00, 0x00, 0xFB}},
		{"DeviceReady", (*EX2).DeviceReady, []byte{0xFA, 0x03, 0x01, 0x00, 0x00, 0x00, 0xFB}},
		{"RequestThermal", (*EX2).RequestThermal, []byte{0xFA, 0x03, 0x08, 0x00, 0x00, 0x00, 0xFB}},
		{"RequestFanStatus", (*EX2).RequestFanStatus, []byte{0xFA, 0x03, 0x09, 0x00, 0x00, 0x00, 0xFB}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := fakemcu.New(t)
			m := NewEX2(OpenFile(fake.Slave))

			done := make(chan error, 1)
			go func() { done <- c.call(m) }()

			got := fake.ReadFrame(t, len(c.want), time.Second)
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if !bytes.Equal(got, c.want) {
				t.Errorf("%s wire bytes = % X, want % X", c.name, got, c.want)
			}
		})
	}
}
