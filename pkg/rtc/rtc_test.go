package rtc

import (
	"bytes"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/pkg/mcu"
	"github.com/symops/wd-control-tool/test/fakemcu"
)

func TestEX4Writer_Set(t *testing.T) {
	fake := fakemcu.New(t)
	w := EX4Writer{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}

	// 2026-11-01 is a Sunday (2026-10-31, used elsewhere in this
	// project's tests/SPEC.md, is a Saturday). dow=1, not 0: EX4 uses
	// Go's time.Weekday()+1 (1=Sunday..7=Saturday), NOT EX2's plain
	// 0-indexed value - see EX4Writer.Set's own doc comment for how
	// that was found (hardware-confirmed, not assumed).
	when := time.Date(2026, time.November, 1, 10, 20, 30, 0, time.UTC)

	done := make(chan error, 1)
	go func() { done <- w.Set(when) }()

	got := fake.ReadFrame(t, 13, 3*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Set: %v", err)
	}

	want, err := mcu.EX4DateTimeFrame(2026, 11, 1, 1, 10, 20, 30)
	if err != nil {
		t.Fatalf("EX4DateTimeFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

// CONFIRMED BUG regression test: Set() used to extract fields
// directly from whatever zone t was tagged with, silently writing
// the wrong absolute moment whenever t wasn't already UTC (every
// other test in this file happens to construct t with time.UTC
// directly, which could never have caught this - a real-hardware
// test comparing `date` against the vendor `rtc -w`'s own "system
// time:" line, exactly 3 hours apart, is what actually found it).
// Same instant as TestEX4Writer_Set's own `when`
// (2026-11-01T10:20:30Z), expressed in a +03 zone instead - must
// produce the IDENTICAL wire bytes.
func TestEX4Writer_Set_ConvertsNonUTCInputToUTC(t *testing.T) {
	fake := fakemcu.New(t)
	w := EX4Writer{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}

	plus3 := time.FixedZone("TEST+03", 3*3600)
	when := time.Date(2026, time.November, 1, 13, 20, 30, 0, plus3) // == 2026-11-01T10:20:30Z

	done := make(chan error, 1)
	go func() { done <- w.Set(when) }()

	got := fake.ReadFrame(t, 13, 3*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Set: %v", err)
	}

	want, err := mcu.EX4DateTimeFrame(2026, 11, 1, 1, 10, 20, 30) // the UTC fields, not the +03 ones; dow=1 not 0, see TestEX4Writer_Set
	if err != nil {
		t.Fatalf("EX4DateTimeFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X (the UTC fields - got the +03 ones instead if this fails)", got, want)
	}
}

// CONFIRMED BUG regression test: this used to assert dow bytes 0..6
// Sun..Sat (plain Go Weekday(), no offset) and claimed that was
// "confirmed day-by-day for all 7 days" - that claim only ever
// checked the byte this method itself wrote, never the vendor's own
// interpretation of it, since EX4 had no read support to check
// against at the time. A live `rtc -w` + `rtc -r` capture on real EX4
// hardware caught the real convention: 1..7 Sun..Sat (Go's Weekday()
// PLUS ONE) - see EX4Writer.Set's own doc comment for the full story.
func TestEX4Writer_Set_WeekdayEncoding(t *testing.T) {
	base := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC) // Sunday
	for i := 0; i < 7; i++ {
		day := base.AddDate(0, 0, i)
		fake := fakemcu.New(t)
		w := EX4Writer{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}

		done := make(chan error, 1)
		go func() { done <- w.Set(day) }()

		got := fake.ReadFrame(t, 13, 3*time.Second)
		if err := <-done; err != nil {
			t.Fatalf("day %d: Set: %v", i, err)
		}
		if want := i + 1; int(got[8]) != want {
			t.Errorf("day %d (%v): dow byte = %d, want %d", i, day.Weekday(), got[8], want)
		}
	}
}

// answerEX2RTCVerifyRead answers the RDateAndTime read that EX2's
// SetDateAndTime now issues right after writing, to verify the write
// took (see pkg/mcu's ex2RTCVerifyAttempts) - with a reply matching
// what was just written, so the caller's Set() succeeds. Built by
// taking EX2DateTimeFrame's own WDateAndTime frame and swapping in
// RDateAndTime's header bytes (B6/B7), since the reply's 7-field BCD
// payload uses the identical sec..year layout either way.
func answerEX2RTCVerifyRead(t *testing.T, fake *fakemcu.Fake, year, mon, dom, dow, hour, min, sec int) {
	t.Helper()
	gotReq := fake.ReadFrame(t, 7, 3*time.Second)
	if wantReq := mcu.EX2DateTimeRequestFrame(); !bytes.Equal(gotReq, wantReq) {
		t.Fatalf("verify request frame = % X, want % X", gotReq, wantReq)
	}
	reply, err := mcu.EX2DateTimeFrame(year, mon, dom, dow, hour, min, sec)
	if err != nil {
		t.Fatalf("EX2DateTimeFrame: %v", err)
	}
	reply[3], reply[4] = 0x01, 0x01 // RDateAndTime's B6/B7, not WDateAndTime's
	if _, err := fake.Master.Write(reply); err != nil {
		t.Fatalf("write fake verify reply: %v", err)
	}
}

func TestEX2Writer_Set(t *testing.T) {
	fake := fakemcu.New(t)
	w := EX2Writer{MCU: mcu.NewEX2(mcu.OpenFile(fake.Slave))}

	// Real-hardware worked example (2026-11-02 Mon 13:38:58) - see
	// pkg/mcu/ex2.go's ex2WDateAndTime doc comment.
	when := time.Date(2026, time.November, 2, 13, 38, 58, 0, time.UTC)

	done := make(chan error, 1)
	go func() { done <- w.Set(when) }()

	got := fake.ReadFrame(t, 13, 3*time.Second)
	want, err := mcu.EX2DateTimeFrame(2026, 11, 2, 1, 13, 38, 58)
	if err != nil {
		t.Fatalf("EX2DateTimeFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}

	// EX2's SetDateAndTime reads straight back after writing, to
	// verify it took (see pkg/mcu's ex2RTCVerifyAttempts) - answer
	// with a reply matching what was just written.
	answerEX2RTCVerifyRead(t, fake, 2026, 11, 2, 1, 13, 38, 58)

	if err := <-done; err != nil {
		t.Fatalf("Set: %v", err)
	}
}

// Same CONFIRMED BUG regression as TestEX4Writer_Set_ConvertsNonUTCInputToUTC,
// for EX2Writer - same instant as TestEX2Writer_Set's own `when`
// (2026-11-02T13:38:58Z), expressed in a +03 zone instead.
func TestEX2Writer_Set_ConvertsNonUTCInputToUTC(t *testing.T) {
	fake := fakemcu.New(t)
	w := EX2Writer{MCU: mcu.NewEX2(mcu.OpenFile(fake.Slave))}

	plus3 := time.FixedZone("TEST+03", 3*3600)
	when := time.Date(2026, time.November, 2, 16, 38, 58, 0, plus3) // == 2026-11-02T13:38:58Z

	done := make(chan error, 1)
	go func() { done <- w.Set(when) }()

	got := fake.ReadFrame(t, 13, 3*time.Second)
	want, err := mcu.EX2DateTimeFrame(2026, 11, 2, 1, 13, 38, 58) // the UTC fields, not the +03 ones
	if err != nil {
		t.Fatalf("EX2DateTimeFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X (the UTC fields - got the +03 ones instead if this fails)", got, want)
	}

	answerEX2RTCVerifyRead(t, fake, 2026, 11, 2, 1, 13, 38, 58)

	if err := <-done; err != nil {
		t.Fatalf("Set: %v", err)
	}
}

func TestEX2Reader_Read(t *testing.T) {
	fake := fakemcu.New(t)
	r := EX2Reader{MCU: mcu.NewEX2(mcu.OpenFile(fake.Slave))}

	type result struct {
		t   time.Time
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, ok, err := r.Read(3 * time.Second)
		done <- result{got, ok, err}
	}()

	fake.ReadFrame(t, 7, 3*time.Second) // drain the RDateAndTime request
	// Real-hardware worked example (2026-11-02 Mon 13:39:43) - see
	// pkg/mcu/ex2.go's EX2DecodeDateAndTime doc comment.
	reply := []byte{0xFA, 0x01, 0x08, 0x01, 0x01, 0x43, 0x39, 0x13, 0x01, 0x02, 0x11, 0x26, 0xFB}
	if _, err := fake.Master.Write(reply); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	res := <-done
	if res.err != nil {
		t.Fatalf("Read: %v", res.err)
	}
	if !res.ok {
		t.Fatal("Read: ok=false, want true (a reply arrived)")
	}
	want := time.Date(2026, time.November, 2, 13, 39, 43, 0, time.UTC) // the RTC stores UTC - see EX4Writer's own doc comment
	if !res.t.Equal(want) {
		t.Errorf("Read = %v, want %v", res.t, want)
	}
}

func TestEX2Reader_Read_IOErrorPropagates(t *testing.T) {
	fake := fakemcu.New(t)
	r := EX2Reader{MCU: mcu.NewEX2(mcu.OpenFile(fake.Slave))}
	fake.Slave.Close()

	if _, ok, err := r.Read(time.Second); err == nil {
		t.Fatalf("Read after Close: want error, got nil (ok=%v)", ok)
	}
}

func TestEX4Reader_Read(t *testing.T) {
	fake := fakemcu.New(t)
	r := EX4Reader{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}

	type result struct {
		t   time.Time
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		got, ok, err := r.Read(3 * time.Second)
		done <- result{got, ok, err}
	}()

	fake.ReadFrame(t, 7, 3*time.Second) // drain the RDateAndTime request
	// Real-hardware worked example (2026-11-04 Wed 20:52:57, as the
	// vendor's own rtc -r printed it, though it mislabeled the
	// weekday as "Tue" - see mcu.EX4DecodeDateAndTime's own doc
	// comment) - the reply's own header does NOT echo the request's
	// (unlike EX2): b7 is 07, not the request's 01.
	reply := []byte{0xFA, 0x01, 0x00, 0x03, 0x07, 0x57, 0x52, 0x20, 0x03, 0x04, 0x11, 0x26, 0xFB}
	if _, err := fake.Master.Write(reply); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	res := <-done
	if res.err != nil {
		t.Fatalf("Read: %v", res.err)
	}
	if !res.ok {
		t.Fatal("Read: ok=false, want true (a reply arrived)")
	}
	want := time.Date(2026, time.November, 4, 20, 52, 57, 0, time.UTC) // the RTC stores UTC - see EX4Writer's own doc comment
	if !res.t.Equal(want) {
		t.Errorf("Read = %v, want %v", res.t, want)
	}
}

func TestEX4Reader_Read_IOErrorPropagates(t *testing.T) {
	fake := fakemcu.New(t)
	r := EX4Reader{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}
	fake.Slave.Close()

	if _, ok, err := r.Read(time.Second); err == nil {
		t.Fatalf("Read after Close: want error, got nil (ok=%v)", ok)
	}
}

func TestEX2Reader_Read_DecodeFailurePropagates(t *testing.T) {
	fake := fakemcu.New(t)
	r := EX2Reader{MCU: mcu.NewEX2(mcu.OpenFile(fake.Slave))}

	done := make(chan struct {
		ok  bool
		err error
	}, 1)
	go func() {
		_, ok, err := r.Read(3 * time.Second)
		done <- struct {
			ok  bool
			err error
		}{ok, err}
	}()

	fake.ReadFrame(t, 7, 3*time.Second) // drain the RDateAndTime request
	// Same genuinely-unrecoverable headless tail as
	// mcu.TestEX2DecodeDateAndTime_RejectsShortOrMalformedReply's
	// siblings.
	unrecoverable := []byte{0x30, 0x00, 0x00, 0x00, 0x00, 0xFB}
	if _, err := fake.Master.Write(unrecoverable); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	r2 := <-done
	if r2.err == nil {
		t.Fatalf("Read with an undecodable reply: want error, got nil (ok=%v)", r2.ok)
	}
	if r2.ok {
		t.Error("Read with an undecodable reply: ok=true, want false")
	}
}

func TestEX2Reader_Read_NoReplyWithinTimeout(t *testing.T) {
	fake := fakemcu.New(t)
	r := EX2Reader{MCU: mcu.NewEX2(mcu.OpenFile(fake.Slave))}

	ok, err := func() (bool, error) {
		_, ok, err := r.Read(10 * time.Millisecond)
		return ok, err
	}()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if ok {
		t.Error("Read: ok=true, want false (nothing was ever written to the fake MCU)")
	}
}
