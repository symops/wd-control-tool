// Package fan controls the cooling fan. The three boards use three
// different mechanisms - see SPEC.md, "Fan control":
//   - EX2U: MCU/UART (pkg/mcu's EX2U client), 5 duty-cycle steps.
//   - EX4:  MCU/UART (pkg/mcu's EX4 client), 5 levels (Stop..Highest).
//   - EX2:  direct /dev/mem register writes (pkg/regio) - the MCU is
//     NOT involved in setting EX2's fan speed at all (confirmed: EX2's
//     vendor LIB_Set_Fan_Speed never touches up_send_ctl/UART).
package fan

// Controller is implemented per board; there is deliberately no
// generic "percent 0-100" abstraction across all three, because the
// boards' real hardware quantizes fan speed differently (5 duty
// steps on EX2U, 5 abstract levels on EX4, 7 fixed RPM targets on
// EX2) and hiding that behind a fake common unit would misrepresent
// what each board can actually do - see SPEC.md.
type Controller interface {
	// Set drives the fan to level, in whatever units this board's
	// hardware natively uses (see the per-board doc comments).
	Set(level int) error
}

// Compile-time check that every per-board type actually satisfies
// Controller - nothing else in this codebase currently asserts this
// (cmd/wd-control-tool/main.go calls each board's MCU/regio client
// directly rather than through this interface), so a signature change
// on any one of them drifting out of sync with Controller would
// otherwise go unnoticed until/unless something tried to use it
// through the interface.
var (
	_ Controller = EX2{}
	_ Controller = EX2U{}
	_ Controller = EX4{}
)
