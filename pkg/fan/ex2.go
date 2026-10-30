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
// Kirkwood-class window LED uses at a different offset):
//
//	reg1 = 0xf1018100, one-hot bits 0x40/0x100/0x800/0x1000/0x10000
//	reg2 = 0xf1018140, one-hot bit  0x1
//
// Clearing mask confirmed as 0x11940 (reg1) / 0x1 (reg2) - exactly
// the union of the bits above, i.e. "stop" clears every fan bit.
//
// Set accepts 0-6 (fan_control -f N's own documented range: 0=Stop,
// 1..6 = RPM steps ~4000..9000 in 1000rpm increments).
//
// CAVEAT: the real binary's jump table confirms these exact 6
// non-zero bitmasks exist and that each level sets exactly one of
// them, but the disassembly dump available to this tool did not
// independently pin down which bitmask belongs to which specific
// level number - the ascending order below is inferred from the jump
// table's natural case order, not independently verified per level.
// The relative ordering (higher level = faster fan) should still
// hold even if a specific level's exact RPM is off; verify against
// real hardware before relying on an exact RPM target. See SPEC.md.
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
	{ex2FanReg1, 0x40},
	{ex2FanReg1, 0x100},
	{ex2FanReg1, 0x800},
	{ex2FanReg1, 0x1000},
	{ex2FanReg1, 0x10000},
	{ex2FanReg2, 0x1},
}

func (f EX2) Set(level int) error {
	if level < 0 || level > 6 {
		return fmt.Errorf("fan: EX2 level must be 0-6, got %d", level)
	}

	// Clear_All_Fan_Register, reproduced exactly.
	if err := f.Mem.SetClearBits(ex2FanReg1, 0, ex2FanClearMask1); err != nil {
		return err
	}
	if err := f.Mem.SetClearBits(ex2FanReg2, 0, ex2FanClearMask2); err != nil {
		return err
	}

	if level == 0 {
		return nil // Stop: every fan bit already cleared above.
	}
	b := ex2FanBits[level-1]
	return f.Mem.SetClearBits(b.reg, b.bit, 0)
}
