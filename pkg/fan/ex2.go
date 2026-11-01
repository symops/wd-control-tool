package fan

import (
	"fmt"

	"github.com/symops/wd-control-tool/pkg/regio"
)

// EX2 drives the fan with direct physical-register writes - no MCU
// involved (confirmed: EX2's vendor LIB_Set_Fan_Speed never calls
// up_send_ctl or touches the UART; its 3 MCU-side FanStop/Half/Full
// commands in pkg/mcu/ex2.go are a separate, apparently-unused-by-
// fan_control control surface). Registers (base 0xf1000000, same
// Kirkwood-class window LED uses):
//
//	reg1 = 0xf1018100, one-hot bits 0x40/0x100/0x800/0x1000/0x10000
//	reg2 = 0xf1018140, one-hot bit  0x1
//
// reg2 is the EXACT SAME physical register pkg/led's hd-bay LED
// scheme calls hdBayReg1 (confirmed via a real register-page scan
// there) - not merely "the same window at a different offset" as an
// earlier version of this comment claimed. The two subsystems share
// this one register on entirely disjoint bits (fan: bit 0 only;
// hd-bay LED: bits 20/21/23/25), which is why this never caused a
// conflict - regio.Mem's Set/Clear writes are atomic read-modify-
// write, so one subsystem's write never clobbers the other's bits.
//
// Clearing mask confirmed as 0x11940 (reg1) / 0x1 (reg2) - exactly
// the union of the bits above, i.e. "stop" clears every fan bit.
//
// Set accepts 0-6 (fan_control -f N's own documented range: 0=Stop,
// 1..6 = RPM steps ~4000..9000 in 1000rpm increments).
//
// Level-to-bitmask order CONFIRMED on real EX2 hardware via the
// vendor fan_control's own `-g` RPM read-back (not just relative
// feel): the disassembly-order guess below was WRONG - verified
// level 2 spinning faster than level 3 on real hardware, then fixed
// by setting each of wd-control-tool's own levels 1-6 in turn and
// reading real RPM after each:
//
//	level 1 = 4411 RPM   level 4 = 7317 RPM
//	level 2 = 5357 RPM   level 5 = 8333 RPM
//	level 3 = 6521 RPM   level 6 = 9090 RPM
//
// (~1000 RPM per step, matching this file's documented ~4000-9000
// range exactly - strong confirmation, not a coincidence). The jump
// table's natural case order had levels 1-6 as
// 0x40,0x100,0x800,0x1000,0x10000,reg2:0x1 - the real order is
// 0x800,0x1000,0x10000,0x100,reg2:0x1,0x40 (see ex2FanBits below).
type EX2 struct{ Mem *regio.Mem }

const (
	ex2FanReg1 = 0xf1018100
	ex2FanReg2 = 0xf1018140

	ex2FanClearMask1 = 0x11940
	ex2FanClearMask2 = 0x1
)

var ex2FanBits = [...]struct {
	reg int64
	bit uint32
}{
	{ex2FanReg1, 0x800},   // level 1, confirmed 4411 RPM
	{ex2FanReg1, 0x1000},  // level 2, confirmed 5357 RPM
	{ex2FanReg1, 0x10000}, // level 3, confirmed 6521 RPM
	{ex2FanReg1, 0x100},   // level 4, confirmed 7317 RPM
	{ex2FanReg2, 0x1},     // level 5, confirmed 8333 RPM
	{ex2FanReg1, 0x40},    // level 6, confirmed 9090 RPM
}

// Ex2FanPlan computes the register writes Set(level) would perform,
// without touching any hardware - the pure half of EX2.Set, used
// directly by the CLI's -n/--dry-run and -D/--debug (and by Set
// itself).
func Ex2FanPlan(level int) ([]regio.Write, error) {
	if level < 0 || level > 6 {
		return nil, fmt.Errorf("fan: EX2 level must be 0-6, got %d", level)
	}

	// Clear_All_Fan_Register, reproduced exactly - always runs, even
	// for level 0, matching the vendor's own unconditional clear.
	ws := []regio.Write{
		{Addr: ex2FanReg1, Clear: ex2FanClearMask1},
		{Addr: ex2FanReg2, Clear: ex2FanClearMask2},
	}
	if level == 0 {
		return ws, nil // Stop: every fan bit already cleared above.
	}
	b := ex2FanBits[level-1]
	return append(ws, regio.Write{Addr: b.reg, Set: b.bit}), nil
}

func (f EX2) Set(level int) error {
	ws, err := Ex2FanPlan(level)
	if err != nil {
		return err
	}
	return f.Mem.ApplyAll(ws)
}
