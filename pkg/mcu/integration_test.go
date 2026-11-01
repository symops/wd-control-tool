package mcu

import (
	"bytes"
	"strings"
	"sync"
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

	// No deliberate inter-byte delay (see ex2u.go) - the timeout here
	// is just generous headroom, not sized to any expected pacing.
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

	// EX4's inter-byte pacing (10us) is negligible, same as EX2U's
	// (which has none at all - see ex2u.go).
	got := fake.ReadFrame(t, 7, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("FanLevel(3): %v", err)
	}

	want := []byte{0xFA, 0x02, 0x00, 0x03, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

// FanLevel's own error-propagation branch (when EX4FanLevelFrame
// rejects an out-of-range level) had no coverage within pkg/mcu
// itself - the mirror image of why
// TestEX4_SetDateAndTime_PropagatesValidationError/
// TestEX4_Display_PropagatesValidationError exist (found via the
// same `go tool cover` pass, this time scoped to pkg/mcu's own
// package coverage rather than the -coverpkg=./pkg/... view).
func TestEX4_FanLevel_PropagatesValidationError(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))
	if err := m.FanLevel(5); err == nil { // valid range is 0-4
		t.Error("FanLevel(5): want error, got nil")
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

// SetDateAndTime's SUCCESS path (sendLegacy actually firing) had no
// coverage within pkg/mcu's own package either - the opposite gap
// from FanLevel's: pkg/rtc's own tests call SetDateAndTime with
// valid values (via rtc.EX4Writer.Set), which covers it in the
// combined -coverpkg=./pkg/... view, but pkg/mcu's own test suite
// never called it with anything but an invalid month. This project's
// own house rule (see BUILDING.md on EX4's SetDateAndTime/FanLevel
// coverage history) is specifically to not lean on a different
// package's tests for a package's own exported method.
func TestEX4_SetDateAndTime_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))

	done := make(chan error, 1)
	go func() { done <- m.SetDateAndTime(2026, 10, 31, 6, 14, 5, 30) }() // worked example from SPEC.md

	got := fake.ReadFrame(t, 13, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("SetDateAndTime: %v", err)
	}

	want := []byte{0xFA, 0x01, 0x00, 0x04, 0x07, 0x30, 0x05, 0x14, 0x06, 0x31, 0x10, 0x26, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

// SetDateAndTime/Display's own error-propagation branch (when the
// underlying EX4DateTimeFrame/EX4DisplayFrame call fails validation)
// was only ever exercised by calling those Frame functions directly
// - never through the methods themselves, which is the path a real
// caller (rtc.EX4Writer.Set, cmd/main.go) actually uses.
func TestEX4_SetDateAndTime_PropagatesValidationError(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))
	if err := m.SetDateAndTime(2026, 13, 1, 0, 0, 0, 0); err == nil { // month 13
		t.Error("SetDateAndTime with month=13: want error, got nil")
	}
}

func TestEX4_Display_PropagatesValidationError(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))
	if err := m.Display(strings.Repeat("A", 256)); err == nil {
		t.Error("Display with 256-byte text: want error, got nil")
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

// Same property as TestEX2U_LEDOnOff_ConcurrentCallsNeverInterleave,
// for the legacy protocol family's own real methods: two goroutines
// repeatedly calling DeviceReady() and FanLevel(2) (both 7-byte
// frames, distinguishable by their differing header/value bytes) on
// one shared *EX4 - no interleaved byte run must ever appear on the
// wire. The legacy protocol's 10us/byte pacing is negligible, so
// this runs fast unlike its EX2U counterpart.
func TestEX4_DeviceReadyAndFanLevel_ConcurrentCallsNeverInterleave(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))

	const rounds = 25
	var wg sync.WaitGroup
	callErrs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := m.DeviceReady(); err != nil {
				callErrs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := m.FanLevel(2); err != nil {
				callErrs <- err
				return
			}
		}
	}()

	got := fake.ReadFrame(t, rounds*2*7, 5*time.Second)

	wg.Wait()
	close(callErrs)
	for err := range callErrs {
		t.Fatalf("DeviceReady/FanLevel: %v", err)
	}

	deviceReady := []byte{0xFA, 0x03, 0x01, 0x00, 0x00, 0x00, 0xFB}
	fanLevel2 := []byte{0xFA, 0x02, 0x00, 0x02, 0x00, 0x00, 0xFB}
	for i := 0; i < len(got); i += 7 {
		chunk := got[i : i+7]
		if !bytes.Equal(chunk, deviceReady) && !bytes.Equal(chunk, fanLevel2) {
			t.Fatalf("byte run at offset %d is neither a clean DeviceReady nor FanLevel(2) frame (interleaved): % X", i, chunk)
		}
	}
}
