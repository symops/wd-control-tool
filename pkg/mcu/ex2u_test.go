package mcu

import (
	"bytes"
	"testing"
)

// Golden-byte tests for EX2U's protocol - this one is hardware-
// confirmed (live strace capture + end-to-end kernel driver test),
// the strongest evidence in this whole codebase. See SPEC.md.

func TestEX2UFrames(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want []byte
	}{
		{"led_set_on", []byte{ex2uStart, 0x03, 0x06, 0x01, 0x00, 0x00, ex2uEnd}, []byte{0xFA, 0x03, 0x06, 0x01, 0x00, 0x00, 0xFB}},
		{"led_set_off", []byte{ex2uStart, 0x03, 0x06, 0x00, 0x00, 0x00, ex2uEnd}, []byte{0xFA, 0x03, 0x06, 0x00, 0x00, 0x00, 0xFB}},
		{"led_blink", []byte{ex2uStart, 0x03, 0x06, 0x02, 0x00, 0x00, ex2uEnd}, []byte{0xFA, 0x03, 0x06, 0x02, 0x00, 0x00, 0xFB}},
		{"led_breath", []byte{ex2uStart, 0x03, 0x06, 0x04, 0x00, 0x00, ex2uEnd}, []byte{0xFA, 0x03, 0x06, 0x04, 0x00, 0x00, 0xFB}},
		{"sys_ready", []byte{ex2uStart, 0x03, 0x01, 0x00, 0x00, 0x00, ex2uEnd}, []byte{0xFA, 0x03, 0x01, 0x00, 0x00, 0x00, 0xFB}},
		{"sys_silent", []byte{ex2uStart, 0x03, 0x03, 0x00, 0x00, 0x00, ex2uEnd}, []byte{0xFA, 0x03, 0x03, 0x00, 0x00, 0x00, 0xFB}},
		{"sys_shutdown", []byte{ex2uStart, 0x03, 0x03, 0x01, 0x00, 0x00, ex2uEnd}, []byte{0xFA, 0x03, 0x03, 0x01, 0x00, 0x00, 0xFB}},
		{"fan_set_50", []byte{ex2uStart, 0x02, 0x00, 0x80, 0x00, 0x00, ex2uEnd}, []byte{0xFA, 0x02, 0x00, 0x80, 0x00, 0x00, 0xFB}},
	}
	for _, c := range cases {
		if !bytes.Equal(c.got, c.want) {
			t.Errorf("%s = % X, want % X", c.name, c.got, c.want)
		}
	}
}

func TestEX2UFanPercent_RejectsUnsupported(t *testing.T) {
	m := &EX2U{} // p is unused by the error path
	for _, bad := range []int{1, 10, 33, 50 + 1, 101, -1} {
		if err := m.FanPercent(bad); err == nil {
			t.Errorf("FanPercent(%d): want error, got nil", bad)
		}
	}
}

// EX2UFanPercentFrame/EX2ULEDFrame are the pure builders the CLI's
// -n/--dry-run and -D/--debug use; pin them against the same golden
// bytes TestEX2UFrames checks, plus their own rejection paths.

func TestEX2UFanPercentFrame(t *testing.T) {
	cases := map[int]byte{0: 0x00, 25: 0x40, 50: 0x80, 75: 0xC0, 100: 0xFF}
	for pct, b := range cases {
		got, err := EX2UFanPercentFrame(pct)
		if err != nil {
			t.Fatalf("EX2UFanPercentFrame(%d): %v", pct, err)
		}
		want := []byte{0xFA, 0x02, 0x00, b, 0x00, 0x00, 0xFB}
		if !bytes.Equal(got, want) {
			t.Errorf("EX2UFanPercentFrame(%d) = % X, want % X", pct, got, want)
		}
	}
}

func TestEX2UFanPercentFrame_RejectsUnsupported(t *testing.T) {
	for _, bad := range []int{1, 10, 33, 51, 101, -1} {
		if _, err := EX2UFanPercentFrame(bad); err == nil {
			t.Errorf("EX2UFanPercentFrame(%d): want error, got nil", bad)
		}
	}
}

func TestEX2ULEDFrame(t *testing.T) {
	cases := map[string]byte{"on": 0x01, "off": 0x00, "blinking": 0x02, "blink": 0x02, "breathing": 0x04, "breathe": 0x04}
	for state, b := range cases {
		got, err := EX2ULEDFrame(state)
		if err != nil {
			t.Fatalf("EX2ULEDFrame(%q): %v", state, err)
		}
		want := []byte{0xFA, 0x03, 0x06, b, 0x00, 0x00, 0xFB}
		if !bytes.Equal(got, want) {
			t.Errorf("EX2ULEDFrame(%q) = % X, want % X", state, got, want)
		}
	}
}

func TestEX2ULEDFrame_RejectsUnknownState(t *testing.T) {
	for _, bad := range []string{"", "ON", "flashing", "breath"} {
		if _, err := EX2ULEDFrame(bad); err == nil {
			t.Errorf("EX2ULEDFrame(%q): want error, got nil", bad)
		}
	}
}
