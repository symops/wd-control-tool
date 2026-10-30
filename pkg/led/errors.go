package led

import "fmt"

func errBadZone(z Zone) error   { return fmt.Errorf("led: invalid zone %v for this board", z) }
func errBadColor(c Color) error { return fmt.Errorf("led: invalid color %v", c) }
func errBadState(s State) error { return fmt.Errorf("led: invalid state %v", s) }
