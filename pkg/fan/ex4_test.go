package fan

import (
	"bytes"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/pkg/mcu"
	"github.com/symops/wd-control-tool/test/fakemcu"
	"github.com/symops/wd-control-tool/test/fakeregio"
)

func TestEX4_Set_OverPTY(t *testing.T) {
	fake := fakemcu.New(t)
	f := EX4{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}

	done := make(chan error, 1)
	go func() { done <- f.Set(2) }()

	got := fake.ReadFrame(t, 7, time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Set(2): %v", err)
	}

	want := []byte{0xFA, 0x02, 0x00, 0x02, 0x00, 0x00, 0xFB}
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X", got, want)
	}
}

func TestEX4_Set_RejectsOutOfRange(t *testing.T) {
	fake := fakemcu.New(t)
	f := EX4{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}
	for _, bad := range []int{-1, 5, 100} {
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%d): want error, got nil", bad)
		}
	}
}

func TestEX4_Set_AllLevels(t *testing.T) {
	for level := 0; level <= 4; level++ {
		fake := fakemcu.New(t)
		f := EX4{MCU: mcu.NewEX4(mcu.OpenFile(fake.Slave))}

		done := make(chan error, 1)
		go func() { done <- f.Set(level) }()

		got := fake.ReadFrame(t, 7, time.Second)
		if err := <-done; err != nil {
			t.Fatalf("Set(%d): %v", level, err)
		}

		want := []byte{0xFA, 0x02, 0x00, byte(level), 0x00, 0x00, 0xFB}
		if !bytes.Equal(got, want) {
			t.Errorf("level %d: wire bytes = % X, want % X", level, got, want)
		}
	}
}

// Mirrors the real libshare.so's getFanPluse exactly (confirmed via
// decompilation - see ex4.go's own doc comment), including its
// asymmetric initial-state seed (1, not "unknown") and its
// single-edge noise rejection.
func TestEx4CountFanEdges(t *testing.T) {
	cases := []struct {
		name    string
		samples []int
		want    int
	}{
		{"no samples", nil, 0},
		{"steady at 1 (matches the initial seed, no edges)", []int{1, 1, 1, 1}, 0},
		{"steady at 0 (one edge from the seed, rejected as noise)", []int{0, 0, 0, 0}, 0},
		{"single real toggle (still just one edge total, rejected)", []int{1, 1, 0, 0}, 0},
		{"one full cycle: 1->0->1 is two edges", []int{1, 0, 1}, 2},
		{"several full cycles", []int{1, 0, 1, 0, 1, 0, 1}, 6},
		{"starts at 0: the seed->0 transition counts too", []int{0, 1, 0, 1}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ex4CountFanEdges(c.samples); got != c.want {
				t.Errorf("ex4CountFanEdges(%v) = %d, want %d", c.samples, got, c.want)
			}
		})
	}
}

// Mirrors the real libshare.so's getFanrpm exactly: edges < 2 -> 0,
// else edges*15 - cross-validated against 3 real EX4 captures (fan
// rpm = 2010, 1980, 2430 all divide evenly by 15: 134, 132, 162
// edges respectively), not just the decompiled formula alone.
func TestEX4DecodeFanRPM(t *testing.T) {
	cases := []struct{ edges, want int }{
		{0, 0},
		{1, 0},
		{2, 30},
		{134, 2010},
		{132, 1980},
		{162, 2430},
	}
	for _, c := range cases {
		if got := EX4DecodeFanRPM(c.edges); got != c.want {
			t.Errorf("EX4DecodeFanRPM(%d) = %d, want %d", c.edges, got, c.want)
		}
	}
}

// Integration-level: exercises the real regio.Mem.ReadReg32 path (via
// fakeregio) end to end, concurrently toggling the tachometer bit a
// known number of times slowly enough that EX4ReadFanPulses's own
// polling (much faster, even through fakeregio's per-call mmap/
// munmap) reliably samples every state - confirming the real wiring
// (register address, bit position, edge counting) works together,
// not just ex4CountFanEdges's own pure-function cases above.
func TestEX4ReadFanPulses_OverFakeRegio(t *testing.T) {
	mem := fakeregio.New(t)

	const toggles = 10
	// 30ms, not the original 5ms - found flaky under `go test -race
	// ./...` (not in isolation: 20/20 clean alone, but 2 failures in
	// 5 full-suite runs, edges as low as 6) when other packages'
	// tests compete for CPU at the same time. A 5ms margin per toggle
	// leaves too little slack against ordinary scheduling jitter
	// under that contention; 30ms gives the busy-poll loop in
	// EX4ReadFanPulses enough room to not miss a toggle even when
	// briefly descheduled. Verified 20/20 under `go test -race
	// -count=1 ./...` (full suite, not just this test) before this
	// change was trusted.
	const toggleInterval = 30 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		bit := uint32(0)
		for i := 0; i < toggles; i++ {
			if err := mem.WriteReg32(EX4FanTachAddr, bit<<EX4FanTachBit); err != nil {
				t.Errorf("WriteReg32: %v", err)
				return
			}
			bit ^= 1
			time.Sleep(toggleInterval)
		}
	}()

	edges, err := EX4ReadFanPulses(mem, toggles*toggleInterval+20*time.Millisecond)
	if err != nil {
		t.Fatalf("EX4ReadFanPulses: %v", err)
	}
	<-done

	// toggles writes starting from 0 (the seed) means toggles edges
	// total (0->1->0->...), not off by one - but real-time
	// scheduling could plausibly miss the very first or last sample,
	// so this allows +/-1 rather than asserting toggles exactly.
	if edges < toggles-1 || edges > toggles+1 {
		t.Errorf("edges = %d, want within 1 of %d", edges, toggles)
	}
}

// EX4ReadFanPulses' own ReadReg32-error branch had no coverage -
// found via `go tool cover`. Closing mem out from under it first
// makes the very first ReadReg32 call fail (mmap against a closed
// fd), which must propagate immediately rather than being swallowed
// into a 0-edge result.
func TestEX4ReadFanPulses_ReadErrorPropagates(t *testing.T) {
	mem := fakeregio.New(t)
	mem.Close()

	if _, err := EX4ReadFanPulses(mem, time.Second); err == nil {
		t.Error("EX4ReadFanPulses on a closed Mem: want error, got nil")
	}
}
