package mcu

import "fmt"

func errUnsupportedFanLevel(v int) error {
	return fmt.Errorf("mcu: unsupported fan level %v for this board", v)
}

func errUnknownCommand(name string) error {
	return fmt.Errorf("mcu: unknown command %q for this board", name)
}
