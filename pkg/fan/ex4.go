package fan

import (
	"time"

	"github.com/symops/wd-control-tool/pkg/mcu"
	"github.com/symops/wd-control-tool/pkg/regio"
)

// EX4 drives the fan via the legacy MCU protocol. Set accepts 0-4
// (Stop, Low, Medium, High, Highest - fan_control -f N's own
// documented range; confirmed as a direct passthrough, see SPEC.md).
type EX4 struct{ MCU *mcu.EX4 }

func (f EX4) Set(level int) error { return f.MCU.FanLevel(level) }

// EX4's fan RPM read-back is NOT an MCU/UART command at all (unlike
// EX2's FanStatusGet) - it's a direct physical-register read, the
// same category of access pkg/regio already provides for EX2's fan
// speed and both boards' LEDs. Confirmed by decompiling the real
// libshare.so (not guessed - see SPEC.md "Fan RPM read-back (EX4)"):
//
//	getFanStatus() = (LIB_Read_CPU_Register(0x10150) >> 7) & 1
//	getFanPluse()  = count of 0/1 transitions of that bit over ~1
//	                 real-world second (a tight busy-poll loop, each
//	                 iteration re-opening/mmapping /dev/mem exactly
//	                 like every other register access in this
//	                 codebase, confirmed by strace showing repeated
//	                 openat+mmap2+close on the same page during a
//	                 real `fan_control -g 4`) - exactly one edge
//	                 over the whole window is treated as 0 (can't
//	                 represent a full tachometer cycle)
//	getFanrpm()    = edges < 2 ? 0 : edges*15
//
// EX4FanTachAddr's physical address (0xf1000000 + 0x10150) matches
// the 0xf1010000 page a live strace showed `fan_control -g 4`
// mmap-ing on real EX4 hardware - not just the disassembly, the
// runtime behavior too.
const (
	// EX4FanTachAddr is typed int64, not left as an untyped constant,
	// deliberately: it doesn't fit in a 32-bit int (confirmed the hard
	// way - go vet/test on this project's amd64 dev host never catches
	// this, only the real armv7/armv5 cross-build does), and an
	// untyped constant passed to an interface{}/any parameter (e.g.
	// fmt.Sprintf) defaults to plain int, not int64, triggering an
	// overflow failure on a 32-bit int platform specifically.
	EX4FanTachAddr          int64 = 0xf1010150
	EX4FanTachBit                 = 7
	EX4FanTachSampleWindow        = time.Second
	ex4FanTachSampleCapHint       = 200000 // vendor's own iteration cap - see ex4CountFanEdges's doc comment
)

// ex4CountFanEdges is the pure edge-counting algorithm
// EX4ReadFanPulses feeds real register samples through - factored
// out so it's directly unit-testable without waiting on a real
// sampling window. samples[i] must each be 0 or 1 (EX4FanTachBit's
// value at one poll). Mirrors the real getFanPluse exactly,
// including its initial seed (the first sample is compared against
// an assumed starting state of 1, not "unknown" - matching the
// vendor's own `iVar6 = 1` before its loop, asymmetric as that is)
// and its single-edge noise rejection.
//
// The vendor's own loop also caps at ex4FanTachSampleCapHint (200000)
// iterations as a secondary exit condition alongside its ~1-second
// wall-clock check - not reproduced here: that count is this
// project's own CPU's observed iterations-per-second at the time it
// was written, not a meaningful hardware constant, and EX4ReadFanPulses
// already bounds itself by wall-clock time (EX4FanTachSampleWindow)
// the same way the vendor's real exit condition does in practice.
func ex4CountFanEdges(samples []int) int {
	last := 1
	edges := 0
	for _, cur := range samples {
		if cur != last {
			edges++
		}
		last = cur
	}
	if edges == 1 {
		edges = 0
	}
	return edges
}

// EX4ReadFanPulses samples the fan tachometer bit as fast as this
// process can manage for window (pass EX4FanTachSampleWindow for the
// vendor-matching ~1 real second), then returns the edge count via
// ex4CountFanEdges.
func EX4ReadFanPulses(m *regio.Mem, window time.Duration) (int, error) {
	samples := make([]int, 0, ex4FanTachSampleCapHint)
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		v, err := m.ReadReg32(EX4FanTachAddr)
		if err != nil {
			return 0, err
		}
		samples = append(samples, int((v>>EX4FanTachBit)&1))
	}
	return ex4CountFanEdges(samples), nil
}

// EX4DecodeFanRPM converts a pulse/edge count (as returned by
// EX4ReadFanPulses) into RPM - mirrors the real getFanrpm exactly.
// The `< 2` branch is redundant with ex4CountFanEdges's own
// single-edge rejection (both exist in the real vendor code as two
// separate functions, so both are kept here too, rather than
// collapsing them and relying on that invariant from outside).
func EX4DecodeFanRPM(edges int) int {
	if edges < 2 {
		return 0
	}
	return edges * 15
}
