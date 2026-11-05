package mcu

import "fmt"

func errUnsupportedFanLevel(v int) error {
	return fmt.Errorf("mcu: unsupported fan level %v for this board", v)
}

func errUnknownLEDState(state string) error {
	return fmt.Errorf("mcu: unknown led state %q", state)
}
