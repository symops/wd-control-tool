package mcu

import (
	"bytes"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/test/fakemcu"
)

// Golden-byte tests for EX2U's protocol - this one is hardware-
// confirmed (live strace capture + end-to-end kernel driver test),
// the strongest evidence in this whole codebase. See SPEC.md.
//
// These call the REAL methods over a real pty (test/fakemcu), not
// two hardcoded byte slices compared against each other - an earlier
// version of this test did exactly that and was nearly tautological
// (it only actually exercised the ex2uStart/ex2uEnd constants, not
// any of send()'s real behavior). This version also covers
// SysReady/SysSilent/SysShutdown/TempRaw, which had no coverage at
// all before - not even a trivial one.
func TestEX2UFrames(t *testing.T) {
	cases := []struct {
		name string
		call func(m *EX2U) error
		want []byte
	}{
		{"LEDOn", (*EX2U).LEDOn, []byte{0xFA, 0x03, 0x06, 0x01, 0x00, 0x00, 0xFB}},
		{"LEDOff", (*EX2U).LEDOff, []byte{0xFA, 0x03, 0x06, 0x00, 0x00, 0x00, 0xFB}},
		{"LEDBlink", (*EX2U).LEDBlink, []byte{0xFA, 0x03, 0x06, 0x02, 0x00, 0x00, 0xFB}},
		{"LEDBreathe", (*EX2U).LEDBreathe, []byte{0xFA, 0x03, 0x06, 0x04, 0x00, 0x00, 0xFB}},
		{"SysReady", (*EX2U).SysReady, []byte{0xFA, 0x03, 0x01, 0x00, 0x00, 0x00, 0xFB}},
		{"SysSilent", (*EX2U).SysSilent, []byte{0xFA, 0x03, 0x03, 0x00, 0x00, 0x00, 0xFB}},
		{"SysShutdown", (*EX2U).SysShutdown, []byte{0xFA, 0x03, 0x03, 0x01, 0x00, 0x00, 0xFB}},
		{"TempRaw", (*EX2U).TempRaw, []byte{0xFA, 0x03, 0x08, 0x00, 0x00, 0x00, 0xFB}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := fakemcu.New(t)
			m := NewEX2U(OpenFile(fake.Slave))

			done := make(chan error, 1)
			go func() { done <- c.call(m) }()

			got := fake.ReadFrame(t, len(c.want), 3*time.Second)
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if !bytes.Equal(got, c.want) {
				t.Errorf("%s wire bytes = % X, want % X", c.name, got, c.want)
			}
		})
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
