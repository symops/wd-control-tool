package mcu

import (
	"bytes"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/test/fakemcu"
)

// Golden-byte tests for EX2's power LED - hardware-confirmed via a
// live strace of the real `led` tool on real EX2 hardware (see
// EX2PowerLEDFrames's own doc comment for the full captured bytes).
func TestEX2PowerLEDFrames(t *testing.T) {
	cases := []struct {
		name  string
		color string
		on    bool
		want  [2][]byte
	}{
		{"blue on", "blue", true, [2][]byte{
			{0xFA, 0x26, 0x00, 0x04, 0x00, 0x01, 0xFB},
			{0xFA, 0x26, 0x00, 0x11, 0x00, 0x01, 0xFB},
		}},
		{"blue off", "blue", false, [2][]byte{
			{0xFA, 0x26, 0x00, 0x04, 0x00, 0x01, 0xFB},
			{0xFA, 0x26, 0x00, 0x01, 0x00, 0x01, 0xFB},
		}},
		{"red on", "red", true, [2][]byte{
			{0xFA, 0x26, 0x00, 0x01, 0x00, 0x01, 0xFB},
			{0xFA, 0x26, 0x00, 0x14, 0x00, 0x01, 0xFB},
		}},
		{"red off", "red", false, [2][]byte{
			{0xFA, 0x26, 0x00, 0x01, 0x00, 0x01, 0xFB},
			{0xFA, 0x26, 0x00, 0x04, 0x00, 0x01, 0xFB},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := EX2PowerLEDFrames(c.color, c.on)
			if err != nil {
				t.Fatalf("EX2PowerLEDFrames(%q, %v): %v", c.color, c.on, err)
			}
			if !bytes.Equal(got[0], c.want[0]) {
				t.Errorf("frame1 = % X, want % X", got[0], c.want[0])
			}
			if !bytes.Equal(got[1], c.want[1]) {
				t.Errorf("frame2 = % X, want % X", got[1], c.want[1])
			}
		})
	}
}

func TestEX2PowerLEDFrames_RejectsUnknownColor(t *testing.T) {
	for _, bad := range []string{"", "green", "BLUE", "Red"} {
		if _, err := EX2PowerLEDFrames(bad, true); err == nil {
			t.Errorf("EX2PowerLEDFrames(%q, true): want error, got nil", bad)
		}
	}
}

// Real send path over a fake MCU, confirming PowerLED emits both
// frames in order on the wire - mirrors TestEX2UFrames' use of a
// real pty rather than comparing two hardcoded byte slices.
func TestEX2_PowerLED_SendsBothFramesInOrder(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2(OpenFile(fake.Slave))

	done := make(chan error, 1)
	go func() { done <- m.PowerLED("blue", true) }()

	got := fake.ReadFrame(t, 14, 3*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("PowerLED: %v", err)
	}
	want := []byte{
		0xFA, 0x26, 0x00, 0x04, 0x00, 0x01, 0xFB,
		0xFA, 0x26, 0x00, 0x11, 0x00, 0x01, 0xFB,
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}
