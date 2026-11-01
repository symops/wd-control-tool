package mcu

import (
	"bytes"
	"strings"
	"sync"
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

// TempReply's real reply framing is unknown (see SPEC.md's Unresolved
// #1) - this only pins the one thing that IS known: the request
// frame sent is tmp_get's fixed bytes, and whatever the other end of
// the wire writes back comes back out verbatim, undecoded. The fake
// MCU here stands in for a real one that might send literally
// anything; this test is not a claim about what a real EX2U actually
// replies with.
// The golden case: the exact bytes captured on real EX2U hardware
// (host MCEX2U-Debian) running this tool's own -t alongside a
// simultaneous `mcu_ctl tmp_get_c`, which reported "Temperature: 30
// C" - see EX2UDecodeTempFahrenheit's doc comment for the reasoning.
func TestEX2UDecodeTempCelsius_RealCapturedReply(t *testing.T) {
	reply := []byte{0xFA, 0x03, 0x08, 0x00, 0x00, 0x56, 0xFB, 0xFA}
	c, err := EX2UDecodeTempCelsius(reply)
	if err != nil {
		t.Fatalf("EX2UDecodeTempCelsius(% X): %v", reply, err)
	}
	if c != 30 {
		t.Errorf("EX2UDecodeTempCelsius(% X) = %d, want 30", reply, c)
	}
	f, err := EX2UDecodeTempFahrenheit(reply)
	if err != nil {
		t.Fatalf("EX2UDecodeTempFahrenheit(% X): %v", reply, err)
	}
	if f != 86 {
		t.Errorf("EX2UDecodeTempFahrenheit(% X) = %d, want 86", reply, f)
	}
}

// Plain rounding-correctness checks, independent of any hardware
// capture: (F-32)*5/9 rounded to the nearest whole degree.
func TestEX2UDecodeTempCelsius_Rounding(t *testing.T) {
	cases := map[byte]int{
		85: 29, // (85-32)*5/9 = 29.44 -> 29
		86: 30, // exact
		87: 31, // (87-32)*5/9 = 30.56 -> 31
		32: 0,  // freezing
	}
	for fahrenheit, want := range cases {
		reply := []byte{0xFA, 0x03, 0x08, 0x00, 0x00, fahrenheit, 0xFB}
		got, err := EX2UDecodeTempCelsius(reply)
		if err != nil {
			t.Fatalf("EX2UDecodeTempCelsius(F=%d): %v", fahrenheit, err)
		}
		if got != want {
			t.Errorf("EX2UDecodeTempCelsius(F=%d) = %d, want %d", fahrenheit, got, want)
		}
	}
}

func TestEX2UDecodeTempFahrenheit_RejectsShortOrMalformedReply(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0xFA, 0x03, 0x08, 0x00, 0x00, 0x56}, // 6 bytes, missing FB
		{0x00, 0x03, 0x08, 0x00, 0x00, 0x56, 0xFB}, // wrong start byte
		{0xFA, 0x03, 0x08, 0x00, 0x00, 0x56, 0x00}, // wrong end byte
	}
	for _, reply := range cases {
		if _, err := EX2UDecodeTempFahrenheit(reply); err == nil {
			t.Errorf("EX2UDecodeTempFahrenheit(% X): want error, got nil", reply)
		}
		if _, err := EX2UDecodeTempCelsius(reply); err == nil {
			t.Errorf("EX2UDecodeTempCelsius(% X): want error, got nil", reply)
		}
	}
}

func TestEX2U_TempReply_ReturnsWhateverRawBytesArrive(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2U(OpenFile(fake.Slave))

	wantReply := []byte{0x11, 0x22, 0x33}
	done := make(chan struct {
		got []byte
		err error
	}, 1)
	go func() {
		got, err := m.TempReply(3 * time.Second)
		done <- struct {
			got []byte
			err error
		}{got, err}
	}()

	gotReq := fake.ReadFrame(t, 7, 3*time.Second)
	wantReq := EX2UTempRequestFrame()
	if !bytes.Equal(gotReq, wantReq) {
		t.Fatalf("request frame = % X, want % X", gotReq, wantReq)
	}

	if _, err := fake.Master.Write(wantReply); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("TempReply: %v", r.err)
	}
	if !bytes.Equal(r.got, wantReply) {
		t.Errorf("TempReply = % X, want % X", r.got, wantReply)
	}
}

// If the MCU never replies (exactly what every real capture of this
// command has shown so far - see SPEC.md), TempReply must return an
// empty, error-free result rather than hanging or failing: a timeout
// here is the NORMAL outcome today, not a bug.
func TestEX2U_TempReply_NoReplyWithinTimeoutReturnsEmptyNoError(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2U(OpenFile(fake.Slave))

	done := make(chan struct {
		got []byte
		err error
	}, 1)
	go func() {
		got, err := m.TempReply(200 * time.Millisecond)
		done <- struct {
			got []byte
			err error
		}{got, err}
	}()

	fake.ReadFrame(t, 7, 3*time.Second) // drain the request; send nothing back

	r := <-done
	if r.err != nil {
		t.Fatalf("TempReply: want nil error on timeout, got %v", r.err)
	}
	if len(r.got) != 0 {
		t.Errorf("TempReply = % X, want empty (no reply arrived)", r.got)
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
		_, err := EX2ULEDFrame(bad)
		if err == nil {
			t.Errorf("EX2ULEDFrame(%q): want error, got nil", bad)
			continue
		}
		// The message must name the actual problem (an unknown LED
		// state) rather than a generic "command" - this was wrong in
		// an earlier version (errUnknownCommand, shared with nothing
		// else) and would have confused a CLI user.
		if !strings.Contains(err.Error(), "led state") {
			t.Errorf("EX2ULEDFrame(%q) error = %q, want it to mention \"led state\"", bad, err.Error())
		}
	}
}

// EX2ULEDFrame takes a free-form string (ultimately CLI-user-
// supplied) - confirm it never panics regardless of content (very
// long strings, unicode, embedded NUL/control bytes, ...) and that
// whenever it DOES accept a value, the result is always exactly the
// 7-byte fixed frame this protocol uses (never, say, a length based
// on the input string's own size, the way EX4DisplayFrame's LEN byte
// legitimately varies).
func FuzzEX2ULEDFrame_NeverPanics(f *testing.F) {
	for _, seed := range []string{"on", "off", "blinking", "breathing", "", "ON", "\x00", "🔥"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, state string) {
		frame, err := EX2ULEDFrame(state)
		if err != nil {
			if frame != nil {
				t.Fatalf("EX2ULEDFrame(%q): non-nil frame alongside an error", state)
			}
			return
		}
		if len(frame) != 7 {
			t.Fatalf("EX2ULEDFrame(%q) = % X, want exactly 7 bytes", state, frame)
		}
	})
}

// serial_test.go's TestPort_SendBytes_ConcurrentFramesNeverInterleave
// proves sendBytes itself never interleaves two frames, but only
// using raw, hand-built byte slices - never through the actual EX2U
// methods a real caller would use. Confirm the same property holds
// through the real call path: two goroutines repeatedly calling
// LEDOn()/LEDOff() (distinguishable 7-byte frames, param byte 0x01
// vs 0x00) on the same *Port - every 7-byte run on the wire must be
// entirely one or the other, never a mix of both methods' bytes.
func TestEX2U_LEDOnOff_ConcurrentCallsNeverInterleave(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX2U(OpenFile(fake.Slave))

	const rounds = 25 // no deliberate inter-byte delay (see ex2u.go), so this is cheap now
	var wg sync.WaitGroup
	callErrs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := m.LEDOn(); err != nil {
				callErrs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := m.LEDOff(); err != nil {
				callErrs <- err
				return
			}
		}
	}()

	got := fake.ReadFrame(t, rounds*2*7, 10*time.Second)

	wg.Wait()
	close(callErrs)
	for err := range callErrs {
		t.Fatalf("LEDOn/LEDOff: %v", err)
	}

	on := []byte{0xFA, 0x03, 0x06, 0x01, 0x00, 0x00, 0xFB}
	off := []byte{0xFA, 0x03, 0x06, 0x00, 0x00, 0x00, 0xFB}
	for i := 0; i < len(got); i += 7 {
		chunk := got[i : i+7]
		if !bytes.Equal(chunk, on) && !bytes.Equal(chunk, off) {
			t.Fatalf("byte run at offset %d is neither a clean LEDOn nor LEDOff frame (interleaved): % X", i, chunk)
		}
	}
}
