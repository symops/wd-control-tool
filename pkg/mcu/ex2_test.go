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

// The real captured byte: fan_control -g 4 printed "fan rpm = 5172"
// while the reply was FA 03 09 00 00 3A FB.
func TestEX2DecodeFanRPM_RealCapturedReply(t *testing.T) {
	reply := []byte{0xFA, 0x03, 0x09, 0x00, 0x00, 0x3A, 0xFB}
	got, err := EX2DecodeFanRPM(reply)
	if err != nil {
		t.Fatalf("EX2DecodeFanRPM(% X): %v", reply, err)
	}
	if got != 5172 {
		t.Errorf("EX2DecodeFanRPM(% X) = %d, want 5172", reply, got)
	}
}

// Cross-validation, not a direct capture: these 6 RPM values were
// each observed as fan_control -g's own printed output (one per fan
// level, read right after setting that level on real hardware - see
// pkg/fan/ex2.go) - their raw reply bytes were never captured
// directly. Still strong evidence for the formula itself: every one
// round-trips EXACTLY through period=round(300000/rpm),
// 300000/period (truncated) - see EX2DecodeFanRPM's doc comment.
func TestEX2DecodeFanRPM_CrossValidatedAgainstMeasuredLevels(t *testing.T) {
	cases := map[byte]int{
		33: 9090,
		41: 7317,
		68: 4411,
		56: 5357,
		46: 6521,
		36: 8333,
	}
	for period, want := range cases {
		reply := []byte{0xFA, 0x03, 0x09, 0x00, 0x00, period, 0xFB}
		got, err := EX2DecodeFanRPM(reply)
		if err != nil {
			t.Fatalf("EX2DecodeFanRPM(period=%d): %v", period, err)
		}
		if got != want {
			t.Errorf("EX2DecodeFanRPM(period=%d) = %d, want %d", period, got, want)
		}
	}
}

func TestEX2DecodeFanRPM_ZeroPeriodIsZeroRPMNotDivideByZero(t *testing.T) {
	reply := []byte{0xFA, 0x03, 0x09, 0x00, 0x00, 0x00, 0xFB}
	got, err := EX2DecodeFanRPM(reply)
	if err != nil {
		t.Fatalf("EX2DecodeFanRPM(period=0): %v", err)
	}
	if got != 0 {
		t.Errorf("EX2DecodeFanRPM(period=0) = %d, want 0", got)
	}
}

// Exact buffers pasted from real EX2 hardware runs of
// `wd-control-tool -b ex2 -f rpm -D`, showing an unsolicited
// `FA 30 00 00 00 00 FB` frame interleaved with FanStatusGet's own
// reply in various ways - see findFrame's doc comment. All of these
// must decode to the right RPM despite the extra bytes.
func TestEX2DecodeFanRPM_RealCapturedNoisyReplies(t *testing.T) {
	cases := []struct {
		name  string
		reply []byte
		want  int
	}{
		{
			"reply then trailing unsolicited frame",
			[]byte{0xFA, 0x03, 0x09, 0x00, 0x00, 0x38, 0xFB, 0xFA, 0x30, 0x00, 0x00, 0x00, 0x00, 0xFB},
			5357,
		},
		{
			"two full copies glued together (leftover from a previous invocation)",
			[]byte{
				0xFA, 0x03, 0x09, 0x00, 0x00, 0x39, 0xFB, 0xFA, 0x30, 0x00, 0x00, 0x00, 0x00, 0xFB,
				0xFA, 0x03, 0x09, 0x00, 0x00, 0x39, 0xFB, 0xFA, 0x30, 0x00, 0x00, 0x00, 0x00, 0xFB,
			},
			5263,
		},
		{
			"reply then a truncated trailing unsolicited frame",
			[]byte{0xFA, 0x03, 0x09, 0x00, 0x00, 0x39, 0xFB, 0xFA},
			5263,
		},
		{
			"unsolicited frame BEFORE the reply (not observed, but the scan must not assume reply-first)",
			[]byte{0xFA, 0x30, 0x00, 0x00, 0x00, 0x00, 0xFB, 0xFA, 0x03, 0x09, 0x00, 0x00, 0x38, 0xFB},
			5357,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := EX2DecodeFanRPM(c.reply)
			if err != nil {
				t.Fatalf("EX2DecodeFanRPM(% X): %v", c.reply, err)
			}
			if got != c.want {
				t.Errorf("EX2DecodeFanRPM(% X) = %d, want %d", c.reply, got, c.want)
			}
		})
	}
}

// The one real case that's genuinely unrecoverable from the bytes
// alone: a previous invocation's leftover tail, missing its own
// head, with the real reply never having arrived in this read at
// all. flushInput (called before the request is sent) is what
// prevents this upstream; decoding can only report it honestly, not
// guess around it.
func TestEX2DecodeFanRPM_RealCapturedHeadlessTailIsUnrecoverable(t *testing.T) {
	reply := []byte{0x30, 0x00, 0x00, 0x00, 0x00, 0xFB}
	if _, err := EX2DecodeFanRPM(reply); err == nil {
		t.Errorf("EX2DecodeFanRPM(% X): want error (no complete matching frame present), got nil", reply)
	}
}

func TestEX2DecodeFanRPM_RejectsShortOrMalformedReply(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0xFA, 0x03, 0x09, 0x00, 0x00, 0x3A}, // 6 bytes, missing FB
		{0x00, 0x03, 0x09, 0x00, 0x00, 0x3A, 0xFB}, // wrong start byte
		{0xFA, 0x03, 0x09, 0x00, 0x00, 0x3A, 0x00}, // wrong end byte
	}
	for _, reply := range cases {
		if _, err := EX2DecodeFanRPM(reply); err == nil {
			t.Errorf("EX2DecodeFanRPM(% X): want error, got nil", reply)
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

// Regression guard for the opposite mistake fixed here: sending
// PowerLED's two frames back-to-back (no pause at all) was found
// unreliable on real hardware - a live strace of one successful
// `led power red on` showed the vendor itself pausing ~400ms between
// them. Confirms the pause actually happens rather than silently
// regressing back to immediate.
func TestEX2_PowerLED_PausesBetweenFrames(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2(OpenFile(fake.Slave))

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- m.PowerLED("blue", true) }()

	fake.ReadFrame(t, 14, 3*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("PowerLED: %v", err)
	}
	if elapsed := time.Since(start); elapsed < ex2PowerLEDInterFrameDelay {
		t.Errorf("PowerLED took %v end-to-end, want at least %v (the inter-frame pause)", elapsed, ex2PowerLEDInterFrameDelay)
	}
}

// PowerLED's own error branches had no coverage: EX2PowerLEDFrames
// failing (bad color) and sendLegacy failing on the first frame.
func TestEX2_PowerLED_RejectsUnknownColor(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2(OpenFile(fake.Slave))
	if err := m.PowerLED("green", true); err == nil {
		t.Error(`PowerLED("green", true): want error, got nil`)
	}
}

func TestEX2_PowerLED_FirstFrameWriteErrorPropagates(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2(OpenFile(fake.Slave))
	fake.Slave.Close()

	if err := m.PowerLED("blue", true); err == nil {
		t.Error("PowerLED on a closed fd: want error, got nil")
	}
}

// FanStatusReply's flushInput error branch had no coverage - close
// the underlying fd first, same technique as
// TestPort_FlushInput_ErrorOnClosedFd.
func TestEX2_FanStatusReply_FlushErrorPropagates(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2(OpenFile(fake.Slave))
	fake.Slave.Close()

	if _, err := m.FanStatusReply(time.Second); err == nil {
		t.Fatal("FanStatusReply after Close: want error, got nil")
	}
}

func TestEX2_FanStatusReply_ReturnsRawReplyBytes(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2(OpenFile(fake.Slave))

	wantReply := []byte{0xFA, 0x03, 0x09, 0x00, 0x00, 0x3A, 0xFB}
	done := make(chan struct {
		got []byte
		err error
	}, 1)
	go func() {
		got, err := m.FanStatusReply(3 * time.Second)
		done <- struct {
			got []byte
			err error
		}{got, err}
	}()

	gotReq := fake.ReadFrame(t, 7, 3*time.Second)
	wantReq := EX2FanStatusRequestFrame()
	if !bytes.Equal(gotReq, wantReq) {
		t.Fatalf("request frame = % X, want % X", gotReq, wantReq)
	}

	if _, err := fake.Master.Write(wantReply); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("FanStatusReply: %v", r.err)
	}
	if !bytes.Equal(r.got, wantReply) {
		t.Errorf("FanStatusReply = % X, want % X", r.got, wantReply)
	}
}
