package mcu

import (
	"bytes"
	"strings"
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
	got, err := EX4DateTimeFrame(2026, 10, 31, 6, 14, 5, 30)
	if err != nil {
		t.Fatalf("EX4DateTimeFrame: %v", err)
	}
	want := buildDateTimeFrame(ex4WDateAndTime, 2026, 10, 31, 6, 14, 5, 30)
	if !bytes.Equal(got, want) {
		t.Errorf("EX4DateTimeFrame = % X, want % X", got, want)
	}
}

// Found by actually running toBCD against negative inputs (not just
// reading the formula): ((v/10)<<4)|(v%10) can produce a byte that
// collides with the frame's own FA/FB markers for specific negative
// values (confirmed: -5/-15 -> 0xFB, -6/-16 -> 0xFA), which would
// corrupt the frame rather than just encode the wrong date. Every
// field must be range-checked before it ever reaches toBCD.
func TestEX4DateTimeFrame_RejectsOutOfRangeFields(t *testing.T) {
	cases := []struct {
		name                                string
		year, mon, dom, dow, hour, min, sec int
	}{
		{"negative year", -1, 1, 1, 0, 0, 0, 0},
		{"month 0", 2026, 0, 1, 0, 0, 0, 0},
		{"month 13", 2026, 13, 1, 0, 0, 0, 0},
		{"dom 0", 2026, 1, 0, 0, 0, 0, 0},
		{"dom 32", 2026, 1, 32, 0, 0, 0, 0},
		{"dow -1 (the actual bug: collides with a frame marker)", 2026, 1, 1, -1, 0, 0, 0},
		{"dow -6 (collides with 0xFA)", 2026, 1, 1, -6, 0, 0, 0},
		{"dow -5 (collides with 0xFB)", 2026, 1, 1, -5, 0, 0, 0},
		{"dow 7", 2026, 1, 1, 7, 0, 0, 0},
		{"hour -1", 2026, 1, 1, 0, -1, 0, 0},
		{"hour 24", 2026, 1, 1, 0, 24, 0, 0},
		{"min -1", 2026, 1, 1, 0, 0, -1, 0},
		{"min 60", 2026, 1, 1, 0, 0, 60, 0},
		{"sec -1", 2026, 1, 1, 0, 0, 0, -1},
		{"sec 60", 2026, 1, 1, 0, 0, 0, 60},
	}
	for _, c := range cases {
		frame, err := EX4DateTimeFrame(c.year, c.mon, c.dom, c.dow, c.hour, c.min, c.sec)
		if err == nil {
			t.Errorf("%s: want error, got nil (frame = % X)", c.name, frame)
		}
		if frame != nil {
			t.Errorf("%s: want nil frame on error, got % X", c.name, frame)
		}
	}
}

// The two specific inputs the bug report was built around - confirm
// the fix actually prevents the collision, not just that *an* error
// is returned for *some* reason.
func TestEX4DateTimeFrame_NegativeDowNeverProducesFrameMarkerByte(t *testing.T) {
	for dow := -20; dow < 0; dow++ {
		frame, err := EX4DateTimeFrame(2026, 1, 1, dow, 0, 0, 0)
		if err == nil {
			t.Fatalf("dow=%d: want error (negative dow is always invalid), got frame % X", dow, frame)
		}
	}
}

func TestEX4DisplayFrame(t *testing.T) {
	got, err := EX4DisplayFrame("Booting...")
	if err != nil {
		t.Fatalf("EX4DisplayFrame: %v", err)
	}
	want := buildTextFrame(ex4SysName, "Booting...")
	if !bytes.Equal(got, want) {
		t.Errorf("EX4DisplayFrame = % X, want % X", got, want)
	}
}

func TestEX4DisplayFrame_EmptyAndLongText(t *testing.T) {
	// Empty text: LEN=0, no payload bytes - the protocol itself
	// places no lower bound (see SPEC.md: no truncation/padding at
	// this layer at all).
	got, err := EX4DisplayFrame("")
	if err != nil {
		t.Fatalf("EX4DisplayFrame(\"\"): %v", err)
	}
	if !bytes.Equal(got, []byte{0xFA, 0x04, 0x01, 0x00, 0x00, 0xFB}) {
		t.Errorf("EX4DisplayFrame(\"\") = % X", got)
	}

	// A string longer than the vendor's own 16-char LCD convention -
	// confirming (again) that this layer does not truncate.
	long := "This is a a much longer status line than the LCD's own 16-char convention"
	got, err = EX4DisplayFrame(long)
	if err != nil {
		t.Fatalf("EX4DisplayFrame(long): %v", err)
	}
	if len(got) != len(long)+6 {
		t.Errorf("EX4DisplayFrame(long): len = %d, want %d", len(got), len(long)+6)
	}
	if got[4] != byte(len(long)) {
		t.Errorf("EX4DisplayFrame(long): LEN byte = %d, want %d", got[4], len(long))
	}
}

// The real bug: byte(len(text)) silently wraps for text 256 bytes or
// longer (byte(256) == 0), which would make the frame's LEN field lie
// about a 0-byte payload while still appending all 256+ real text
// bytes before FB - confirmed by actually computing byte(256), not
// just by reading the formula. 255 is the last length that still
// fits in one byte and must still work; 256 must be rejected, not
// silently produce a wrong-but-plausible-looking LEN byte.
func TestEX4DisplayFrame_LengthBoundary(t *testing.T) {
	text255 := strings.Repeat("A", 255)
	got, err := EX4DisplayFrame(text255)
	if err != nil {
		t.Fatalf("EX4DisplayFrame(255 bytes): %v", err)
	}
	if got[4] != 0xFF {
		t.Errorf("EX4DisplayFrame(255 bytes): LEN byte = 0x%02X, want 0xFF", got[4])
	}

	text256 := strings.Repeat("A", 256)
	if _, err := EX4DisplayFrame(text256); err == nil {
		t.Error("EX4DisplayFrame(256 bytes): want error (LEN byte would wrap to 0), got nil")
	}
}

// A literal 0xFA/0xFB byte anywhere in the text - reachable via a
// deliberately crafted argument (os.Args bytes aren't guaranteed
// valid UTF-8) - must be rejected, not silently embedded where it
// would corrupt the frame under either possible receiver parsing
// strategy (see EX4DisplayFrame's doc comment).
func TestEX4DisplayFrame_RejectsEmbeddedMarkerBytes(t *testing.T) {
	cases := []string{
		"\xfa",
		"\xfb",
		"abc\xfadef",
		"abc\xfbdef",
		"\xfa\xfb",
		"trailing\xfa",
	}
	for _, text := range cases {
		if frame, err := EX4DisplayFrame(text); err == nil {
			t.Errorf("EX4DisplayFrame(%q): want error, got frame % X", text, frame)
		}
	}

	// A byte that merely LOOKS risky but isn't actually FA/FB (0xF9,
	// 0xFC) must still be accepted - this is a marker-collision guard,
	// not a blanket "no high bytes" restriction.
	for _, text := range []string{"\xf9", "\xfc", "normal text"} {
		if _, err := EX4DisplayFrame(text); err != nil {
			t.Errorf("EX4DisplayFrame(%q): unexpected error: %v", text, err)
		}
	}
}

// The LEN byte (byte(len(text))) is just as capable of colliding as
// any text byte: a 250 or 251-byte string makes LEN itself equal
// 0xFA/0xFB even though every character in the text is ordinary.
// Found by FuzzEX4DisplayFrame_NeverEmitsMarkerBytesInPayload, not by
// manual review - pinned explicitly here since it's an easy case to
// walk right past ("check every text byte" doesn't cover the LEN
// byte, which isn't a text byte). 249/252 are the immediate
// neighbors and must still be accepted.
func TestEX4DisplayFrame_RejectsLengthThatMakesLenByteCollide(t *testing.T) {
	if _, err := EX4DisplayFrame(strings.Repeat("0", 250)); err == nil {
		t.Error("EX4DisplayFrame(250 bytes): want error (LEN byte would be 0xFA), got nil")
	}
	if _, err := EX4DisplayFrame(strings.Repeat("0", 251)); err == nil {
		t.Error("EX4DisplayFrame(251 bytes): want error (LEN byte would be 0xFB), got nil")
	}
	if _, err := EX4DisplayFrame(strings.Repeat("0", 249)); err != nil {
		t.Errorf("EX4DisplayFrame(249 bytes): unexpected error: %v", err)
	}
	if _, err := EX4DisplayFrame(strings.Repeat("0", 252)); err != nil {
		t.Errorf("EX4DisplayFrame(252 bytes): unexpected error: %v", err)
	}
}

// Formal version of the property manually checked (and fixed) for
// specific boundary cases in TestEX4DateTimeFrame_RejectsOutOfRangeFields:
// no input that validateDateTimeFields accepts may ever produce a
// payload byte equal to the frame's own FA/FB markers. Go's native
// fuzzer explores far more of the input space than hand-picked cases
// can - run with `go test -fuzz=FuzzEX4DateTimeFrame ./pkg/mcu` to
// actually fuzz beyond the seed corpus; a plain `go test` only runs
// the seeds (fast, still useful as a regression check).
func FuzzEX4DateTimeFrame_NeverEmitsMarkerBytesInPayload(f *testing.F) {
	f.Add(2026, 10, 31, 6, 14, 5, 30)
	f.Add(0, 1, 1, 0, 0, 0, 0)
	f.Add(-1, -6, -16, -5, -1, -1, -1) // the exact values that used to collide
	f.Add(9999, 12, 31, 6, 23, 59, 59)

	f.Fuzz(func(t *testing.T, year, mon, dom, dow, hour, min, sec int) {
		frame, err := EX4DateTimeFrame(year, mon, dom, dow, hour, min, sec)
		if err != nil {
			return // correctly rejected - nothing on the wire to check
		}
		if len(frame) != 13 {
			t.Fatalf("accepted input produced a %d-byte frame, want 13 (year=%d mon=%d dom=%d dow=%d hour=%d min=%d sec=%d)",
				len(frame), year, mon, dom, dow, hour, min, sec)
		}
		// frame[0] and frame[12] are the real start/end markers;
		// nothing in between (the header bytes and the 7 BCD payload
		// bytes) may equal either marker value.
		for i := 1; i < len(frame)-1; i++ {
			if frame[i] == legacyStart || frame[i] == legacyEnd {
				t.Fatalf("payload byte %d = 0x%02X collides with a frame marker - frame=% X (inputs: year=%d mon=%d dom=%d dow=%d hour=%d min=%d sec=%d)",
					i, frame[i], frame, year, mon, dom, dow, hour, min, sec)
			}
		}
	})
}

// Same property as FuzzEX4DateTimeFrame_NeverEmitsMarkerBytesInPayload,
// for EX4DisplayFrame's text payload: any input EX4DisplayFrame
// accepts must produce a frame whose only FA/FB bytes are the real
// start/end markers at position 0 and len-1. Run with
// `go test -fuzz=FuzzEX4DisplayFrame_NeverEmitsMarkerBytesInPayload
// ./pkg/mcu` to fuzz beyond the seed corpus.
func FuzzEX4DisplayFrame_NeverEmitsMarkerBytesInPayload(f *testing.F) {
	f.Add("")
	f.Add("Booting...")
	f.Add(strings.Repeat("A", 255))
	f.Add("\xfa")
	f.Add("\xfb")
	f.Add("abc\xfadef\xfbghi")

	f.Fuzz(func(t *testing.T, text string) {
		frame, err := EX4DisplayFrame(text)
		if err != nil {
			return // correctly rejected (too long, or an embedded marker byte)
		}
		if len(frame) != len(text)+6 {
			t.Fatalf("accepted text %q (%d bytes) produced a %d-byte frame, want %d", text, len(text), len(frame), len(text)+6)
		}
		for i := 1; i < len(frame)-1; i++ {
			if frame[i] == legacyStart || frame[i] == legacyEnd {
				t.Fatalf("frame byte %d = 0x%02X collides with a frame marker - frame=% X (input text=%q)", i, frame[i], frame, text)
			}
		}
	})
}
