package mcu

import "fmt"

func errUnsupportedFanLevel(v int) error {
	return fmt.Errorf("mcu: unsupported fan level %v for this board", v)
}

func errUnknownLEDState(state string) error {
	return fmt.Errorf("mcu: unknown led state %q", state)
}

// ErrEX2PowerLEDBlinkingUnconfirmed: EX2PowerLEDFrames only has
// captured frames for on/off (see its doc comment) - blinking is
// reported as an error rather than guessed.
var ErrEX2PowerLEDBlinkingUnconfirmed = fmt.Errorf("mcu: EX2 power LED blinking has no confirmed MCU frame (only on/off were captured) - see SPEC.md")
