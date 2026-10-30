package mcu

import (
	"bytes"
	"testing"
)

// Golden-byte tests against the worked examples in SPEC.md - these
// are not guesses, they're the exact bytes confirmed via disassembly
// in this project's own RE work. If these ever fail after an edit to
// legacy.go/ex4.go, the fix is to revert the edit, not the test.

func TestBuildValueFrame_EX4FanControl(t *testing.T) {
	got := buildValueFrame(ex4FanControl, 3)
	want := []byte{0xFA, 0x02, 0x00, 0x03, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("FanControl 3 = % X, want % X", got, want)
	}
}

func TestBuildFixedFrame_EX4DeviceReady(t *testing.T) {
	got := buildFixedFrame(ex4DeviceReady)
	want := []byte{0xFA, 0x03, 0x01, 0x00, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("DeviceReady = % X, want % X", got, want)
	}
}

func TestBuildFixedFrame_EX4RDateAndTime(t *testing.T) {
	got := buildFixedFrame(ex4RDateAndTime)
	want := []byte{0xFA, 0x01, 0x00, 0x03, 0x01, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("RDateAndTime = % X, want % X", got, want)
	}
}

func TestBuildDateTimeFrame_EX4(t *testing.T) {
	// Sat 2026-10-31 14:05:30.
	got := buildDateTimeFrame(ex4WDateAndTime, 2026, 10, 31, 6, 14, 5, 30)
	want := []byte{0xFA, 0x01, 0x00, 0x04, 0x07, 0x30, 0x05, 0x14, 0x06, 0x31, 0x10, 0x26, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("WDateAndTime = % X, want % X", got, want)
	}
}

func TestToBCD(t *testing.T) {
	cases := map[int]byte{0: 0x00, 9: 0x09, 10: 0x10, 30: 0x30, 59: 0x59, 99: 0x99}
	for v, want := range cases {
		if got := toBCD(v); got != want {
			t.Errorf("toBCD(%d) = 0x%02X, want 0x%02X", v, got, want)
		}
	}
}

func TestBuildTextFrame_EX4SysName(t *testing.T) {
	got := buildTextFrame(ex4SysName, "Booting...")
	want := []byte{0xFA, 0x04, 0x01, 0x00, 0x0A}
	want = append(want, "Booting..."...)
	want = append(want, 0xFB)
	if !bytes.Equal(got, want) {
		t.Errorf("SysName = % X, want % X", got, want)
	}
}

func TestBuildFixedFrame_EX2FanCommands(t *testing.T) {
	cases := []struct {
		name string
		cmd  legacyCmd
		b6   byte
	}{
		{"FanStop", ex2FanStop, 0x00},
		{"FanHalf", ex2FanHalf, 0x01},
		{"FanFull", ex2FanFull, 0x02},
	}
	for _, c := range cases {
		got := buildFixedFrame(c.cmd)
		want := []byte{0xFA, 0x02, 0x00, c.b6, 0x00, 0x00, 0xFB}
		if !bytes.Equal(got, want) {
			t.Errorf("%s = % X, want % X", c.name, got, want)
		}
	}
}

// Exported pure frame-builder functions (added for the CLI's
// -n/--dry-run and -D/--debug, which must compute these bytes without
// opening a serial port) must agree exactly with the already-golden
// values above - they're thin wrappers over the same private
// builders, but a future edit could desync them, so pin both ends.

func TestEX4FanLevelFrame(t *testing.T) {
	got, err := EX4FanLevelFrame(3)
	if err != nil {
		t.Fatalf("EX4FanLevelFrame(3): %v", err)
	}
	want := buildValueFrame(ex4FanControl, 3)
	if !bytes.Equal(got, want) {
		t.Errorf("EX4FanLevelFrame(3) = % X, want % X", got, want)
	}
}

func TestEX4FanLevelFrame_RejectsOutOfRange(t *testing.T) {
	for _, bad := range []int{-1, 5, 100} {
		if _, err := EX4FanLevelFrame(bad); err == nil {
			t.Errorf("EX4FanLevelFrame(%d): want error, got nil", bad)
		}
	}
}

func TestEX4DateTimeFrame(t *testing.T) {
	got := EX4DateTimeFrame(2026, 10, 31, 6, 14, 5, 30)
	want := buildDateTimeFrame(ex4WDateAndTime, 2026, 10, 31, 6, 14, 5, 30)
	if !bytes.Equal(got, want) {
		t.Errorf("EX4DateTimeFrame = % X, want % X", got, want)
	}
}

func TestEX4DisplayFrame(t *testing.T) {
	got := EX4DisplayFrame("Booting...")
	want := buildTextFrame(ex4SysName, "Booting...")
	if !bytes.Equal(got, want) {
		t.Errorf("EX4DisplayFrame = % X, want % X", got, want)
	}
}

func TestEX4DisplayFrame_EmptyAndLongText(t *testing.T) {
	// Empty text: LEN=0, no payload bytes - the protocol itself
	// places no lower bound (see SPEC.md: no truncation/padding at
	// this layer at all).
	if got := EX4DisplayFrame(""); !bytes.Equal(got, []byte{0xFA, 0x04, 0x01, 0x00, 0x00, 0xFB}) {
		t.Errorf("EX4DisplayFrame(\"\") = % X", got)
	}

	// A string longer than the vendor's own 16-char LCD convention -
	// confirming (again) that this layer does not truncate.
	long := "This is a a much longer status line than the LCD's own 16-char convention"
	got := EX4DisplayFrame(long)
	if len(got) != len(long)+6 {
		t.Errorf("EX4DisplayFrame(long): len = %d, want %d", len(got), len(long)+6)
	}
	if got[4] != byte(len(long)) {
		t.Errorf("EX4DisplayFrame(long): LEN byte = %d, want %d", got[4], len(long))
	}
}
