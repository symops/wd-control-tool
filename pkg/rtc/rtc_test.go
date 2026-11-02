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
	// project's tests/SPEC.md, is a Saturday) - time.Weekday's
	// encoding (0=Sunday) is used directly as the wire's dow field,
	// so this also pins that no remapping is needed/happening.
	when := time.Date(2026, time.November, 1, 10, 20, 30, 0, time.UTC)

	done := make(chan error, 1)
	go func() { done <- w.Set(when) }()

	got := fake.ReadFrame(t, 13, 3*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Set: %v", err)
	}

	want, err := mcu.EX4DateTimeFrame(2026, 11, 1, 0, 10, 20, 30)
	if err != nil {
		t.Fatalf("EX4DateTimeFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX4Writer_Set_WeekdayEncoding(t *testing.T) {
	// Confirm every day of one week maps dow 0..6 Sun..Sat, matching
	// Go's own time.Weekday numbering, with no off-by-one.
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
		if int(got[8]) != i {
			t.Errorf("day %d (%v): dow byte = %d, want %d", i, day.Weekday(), got[8], i)
		}
	}
}

func TestErrEX4ReadNotImplemented(t *testing.T) {
	if ErrEX4ReadNotImplemented == nil {
		t.Fatal("ErrEX4ReadNotImplemented is nil")
	}
	if ErrEX4ReadNotImplemented.Error() == "" {
		t.Error("ErrEX4ReadNotImplemented has an empty message")
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
	if err := <-done; err != nil {
		t.Fatalf("Set: %v", err)
	}

	want, err := mcu.EX2DateTimeFrame(2026, 11, 2, 1, 13, 38, 58)
	if err != nil {
		t.Fatalf("EX2DateTimeFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
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
	want := time.Date(2026, time.November, 2, 13, 39, 43, 0, time.Local)
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
