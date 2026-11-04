package mcu

import (
	"bytes"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/test/fakemcu"
)

func TestEX4DateTimeRequestFrame(t *testing.T) {
	// Real capture of `rtc -r` on real EX4 hardware - see
	// ex4RDateAndTime's own doc comment.
	want := []byte{0xFA, 0x01, 0x00, 0x03, 0x01, 0x00, 0xFB}
	if got := EX4DateTimeRequestFrame(); !bytes.Equal(got, want) {
		t.Errorf("EX4DateTimeRequestFrame() = % X, want % X", got, want)
	}
}

// Golden-byte test against the real capture of `rtc -r` while the
// MCU's RTC itself read 2026/11/4 (the vendor tool's own printed
// "Tue" is wrong relative to the real calendar - 2026-11-04 is a
// Wednesday - see EX4DecodeDateAndTime's own doc comment for why:
// the vendor's weekday-name table expects dow 1=Sunday, this decoder
// just returns the raw byte either way, same as EX2DecodeDateAndTime).
func TestEX4DecodeDateAndTime_RealCapturedReply(t *testing.T) {
	reply := []byte{0xFA, 0x01, 0x00, 0x03, 0x07, 0x57, 0x52, 0x20, 0x03, 0x04, 0x11, 0x26, 0xFB}
	year, mon, dom, dow, hour, min, sec, err := EX4DecodeDateAndTime(reply)
	if err != nil {
		t.Fatalf("EX4DecodeDateAndTime: %v", err)
	}
	if year != 2026 || mon != 11 || dom != 4 || dow != 3 || hour != 20 || min != 52 || sec != 57 {
		t.Errorf("got year=%d mon=%d dom=%d dow=%d hour=%d min=%d sec=%d, want 2026/11/4 dow=3 20:52:57",
			year, mon, dom, dow, hour, min, sec)
	}
}

// Same noisy-buffer shape confirmed for EX2's own RDateAndTime reply
// (see TestEX2DecodeDateAndTime_SkipsUnsolicitedNoiseFrame) - not
// re-confirmed independently for EX4 by a live capture showing the
// noise frame specifically, but findFrame's scanning behavior is
// board-agnostic, so this pins that EX4DecodeDateAndTime actually
// uses it rather than assuming an offset.
func TestEX4DecodeDateAndTime_SkipsUnsolicitedNoiseFrame(t *testing.T) {
	noise := []byte{0xFA, 0x30, 0x00, 0x00, 0x00, 0x00, 0xFB}
	real := []byte{0xFA, 0x01, 0x00, 0x03, 0x07, 0x57, 0x52, 0x20, 0x03, 0x04, 0x11, 0x26, 0xFB}

	for _, reply := range [][]byte{
		append(append([]byte{}, noise...), real...),
		append(append([]byte{}, real...), noise...),
	} {
		year, _, _, _, _, _, _, err := EX4DecodeDateAndTime(reply)
		if err != nil {
			t.Fatalf("EX4DecodeDateAndTime(% X): %v", reply, err)
		}
		if year != 2026 {
			t.Errorf("EX4DecodeDateAndTime(% X): year=%d, want 2026", reply, year)
		}
	}
}

func TestEX4DecodeDateAndTime_RejectsShortOrMalformedReply(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0xFA, 0x01, 0x00, 0x03, 0x07, 0x57, 0x52, 0x20, 0x03, 0x04, 0x11, 0x26},       // 12 bytes, missing FB
		{0x00, 0x01, 0x00, 0x03, 0x07, 0x57, 0x52, 0x20, 0x03, 0x04, 0x11, 0x26, 0xFB}, // wrong start byte
		{0xFA, 0x01, 0x00, 0x03, 0x01, 0x57, 0x52, 0x20, 0x03, 0x04, 0x11, 0x26, 0xFB}, // the REQUEST's own b7 (01), not the reply's (07)
	}
	for _, reply := range cases {
		if _, _, _, _, _, _, _, err := EX4DecodeDateAndTime(reply); err == nil {
			t.Errorf("EX4DecodeDateAndTime(% X): want error, got nil", reply)
		}
	}
}

func TestEX4_DateAndTimeReply_FlushErrorPropagates(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))
	fake.Slave.Close()

	if _, err := m.DateAndTimeReply(time.Second); err == nil {
		t.Fatal("DateAndTimeReply after Close: want error, got nil")
	}
}

func TestEX4_DateAndTimeReply_ReturnsRawReplyBytes(t *testing.T) {
	fake := fakemcu.New(t)
	m := NewEX4(OpenFile(fake.Slave))

	wantReply := []byte{0xFA, 0x01, 0x00, 0x03, 0x07, 0x57, 0x52, 0x20, 0x03, 0x04, 0x11, 0x26, 0xFB}
	done := make(chan struct {
		got []byte
		err error
	}, 1)
	go func() {
		got, err := m.DateAndTimeReply(3 * time.Second)
		done <- struct {
			got []byte
			err error
		}{got, err}
	}()

	gotReq := fake.ReadFrame(t, 7, 3*time.Second)
	wantReq := EX4DateTimeRequestFrame()
	if !bytes.Equal(gotReq, wantReq) {
		t.Fatalf("request frame = % X, want % X", gotReq, wantReq)
	}

	if _, err := fake.Master.Write(wantReply); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("DateAndTimeReply: %v", r.err)
	}
	if !bytes.Equal(r.got, wantReply) {
		t.Errorf("DateAndTimeReply = % X, want % X", r.got, wantReply)
	}
}
