package led

import "fmt"

// These report the raw numeric value, not %v - Zone/Color/State all
// implement String(), which makes %v print "unknown" for exactly the
// out-of-range values these helpers exist to report, defeating the
// point of the error message. (Found by actually running one of
// these through fmt.Println - see pkg/led's own tests.)
func errBadZone(z Zone) error   { return fmt.Errorf("led: invalid zone %d", int(z)) }
func errBadColor(c Color) error { return fmt.Errorf("led: invalid color %d", int(c)) }
func errBadState(s State) error { return fmt.Errorf("led: invalid state %d", int(s)) }
