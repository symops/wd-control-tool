// wd-control-tool is a self-contained replacement for the proprietary
// fan_control/rtc/led/up_send_ctl (EX2/EX4) and mcu_ctl (EX2U) tools -
// no xmldb, no up_read_daemon/up_send_daemon, no vendor .so stack.
// See SPEC.md for the full per-board hardware reference and README.md
// for the CLI reference this file implements.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/symops/wd-control-tool/pkg/board"
	"github.com/symops/wd-control-tool/pkg/fan"
	"github.com/symops/wd-control-tool/pkg/led"
	"github.com/symops/wd-control-tool/pkg/mcu"
	"github.com/symops/wd-control-tool/pkg/regio"
	"github.com/symops/wd-control-tool/pkg/rtc"
)

// Exit codes, verona-tool style.
const (
	exitOK          = 0
	exitUsage       = 1
	exitUnsupported = 2
	exitHardwareIO  = 3
)

const version = "0.1.0-dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("wd-control-tool", flag.ContinueOnError)
	boardFlag := fs.String("b", "", "board: ex2u, ex2 or ex4 (required)")
	fs.StringVar(boardFlag, "board", "", "alias for -b")
	serialPath := fs.String("serial", "/dev/ttyS1", "MCU serial device path (confirmed /dev/ttyS1 on EX2/EX4; EX2U's uart1 tty node is NOT yet confirmed - override with this flag if /dev/ttyS1 is wrong)")
	showVersion := fs.Bool("v", false, "show version and exit")
	fs.BoolVar(showVersion, "version", false, "alias for -v")
	fs.Usage = func() { printUsage(fs) }

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *showVersion {
		fmt.Printf("wd-control-tool %s\nSymo <symops@gmail.com>\n", version)
		return exitOK
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(fs)
		return exitUsage
	}

	if *boardFlag == "" {
		fmt.Fprintln(os.Stderr, "wd-control-tool: -b/--board is required")
		return exitUsage
	}
	b, err := board.Parse(*boardFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitUsage
	}

	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "fan":
		return cmdFan(b, *serialPath, cmdArgs)
	case "led":
		return cmdLED(b, *serialPath, cmdArgs)
	case "display":
		return cmdDisplay(b, *serialPath, cmdArgs)
	case "rtc":
		return cmdRTC(b, *serialPath, cmdArgs)
	default:
		fmt.Fprintf(os.Stderr, "wd-control-tool: unknown command %q\n", cmd)
		printUsage(fs)
		return exitUsage
	}
}

func printUsage(fs *flag.FlagSet) {
	fmt.Fprint(os.Stderr, `wd-control-tool -b BOARD <command> [args...]

Boards: ex2u, ex2, ex4

Commands:
  fan LEVEL                     set fan speed (board-native units - see README.md)
  led ZONE COLOR STATE          set an LED (ex2/ex4 only; zones: power,hd0,hd1[,hd2,hd3]; colors: red,blue; states: on,off,blinking)
  display TEXT                  write text to the front-panel LCD (ex4 only)
  rtc write                     write the host's current time to the MCU RTC (ex2u,ex4; ex2 not yet implemented - see SPEC.md)

`)
	fs.PrintDefaults()
}

func openMem() (*regio.Mem, int) {
	m, err := regio.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return nil, exitHardwareIO
	}
	return m, exitOK
}

func openPort(path string, baud int) (*mcu.Port, int) {
	p, err := mcu.Open(path, baud)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return nil, exitHardwareIO
	}
	return p, exitOK
}

func cmdFan(b board.Board, serialPath string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: wd-control-tool -b BOARD fan LEVEL")
		return exitUsage
	}
	level, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool: fan level must be an integer:", err)
		return exitUsage
	}

	switch b {
	case board.EX2U:
		p, code := openPort(serialPath, 19200)
		if p == nil {
			return code
		}
		defer p.Close()
		if err := (fan.EX2U{MCU: mcu.NewEX2U(p)}).Set(level); err != nil {
			fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
			return exitHardwareIO
		}
	case board.EX4:
		p, code := openPort(serialPath, 19200)
		if p == nil {
			return code
		}
		defer p.Close()
		if err := (fan.EX4{MCU: mcu.NewEX4(p)}).Set(level); err != nil {
			fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
			return exitHardwareIO
		}
	case board.EX2:
		m, code := openMem()
		if m == nil {
			return code
		}
		defer m.Close()
		if err := (fan.EX2{Mem: m}).Set(level); err != nil {
			fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
			return exitHardwareIO
		}
	default:
		return exitUnsupported
	}
	return exitOK
}

func cmdLED(b board.Board, serialPath string, args []string) int {
	if b == board.EX2U {
		return cmdLEDEX2U(serialPath, args)
	}
	if b != board.EX2 && b != board.EX4 {
		return exitUnsupported
	}
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: wd-control-tool -b BOARD led ZONE COLOR STATE")
		return exitUsage
	}
	zone, err := parseZone(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitUsage
	}
	if (zone == led.HD2 || zone == led.HD3) && b == board.EX2 {
		fmt.Fprintln(os.Stderr, "wd-control-tool: EX2 only has hd0/hd1, not", args[0])
		return exitUnsupported
	}
	color, err := parseColor(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitUsage
	}
	state, err := parseState(args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitUsage
	}

	m, code := openMem()
	if m == nil {
		return code
	}
	defer m.Close()
	if err := led.Set(m, zone, color, state); err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitHardwareIO
	}
	return exitOK
}

func cmdLEDEX2U(serialPath string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: wd-control-tool -b ex2u led STATE   (STATE: on, off, blinking, breathing)")
		return exitUsage
	}
	p, code := openPort(serialPath, 19200)
	if p == nil {
		return code
	}
	defer p.Close()
	m := mcu.NewEX2U(p)
	var err error
	switch args[0] {
	case "on":
		err = m.LEDOn()
	case "off":
		err = m.LEDOff()
	case "blinking", "blink":
		err = m.LEDBlink()
	case "breathing", "breathe":
		err = m.LEDBreathe()
	default:
		fmt.Fprintln(os.Stderr, "wd-control-tool: unknown led state", args[0])
		return exitUsage
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitHardwareIO
	}
	return exitOK
}

func cmdDisplay(b board.Board, serialPath string, args []string) int {
	if !b.HasDisplay() {
		fmt.Fprintln(os.Stderr, "wd-control-tool: board", b, "has no display")
		return exitUnsupported
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: wd-control-tool -b ex4 display TEXT")
		return exitUsage
	}
	p, code := openPort(serialPath, 19200)
	if p == nil {
		return code
	}
	defer p.Close()
	if err := mcu.NewEX4(p).Display(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitHardwareIO
	}
	return exitOK
}

func cmdRTC(b board.Board, serialPath string, args []string) int {
	if len(args) != 1 || args[0] != "write" {
		fmt.Fprintln(os.Stderr, "usage: wd-control-tool -b BOARD rtc write")
		return exitUsage
	}
	switch b {
	case board.EX4:
		p, code := openPort(serialPath, 19200)
		if p == nil {
			return code
		}
		defer p.Close()
		w := rtc.EX4Writer{MCU: mcu.NewEX4(p)}
		if err := w.Set(time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
			return exitHardwareIO
		}
	case board.EX2:
		fmt.Fprintln(os.Stderr, "wd-control-tool:", rtc.ErrEX2NotImplemented)
		return exitUnsupported
	default:
		return exitUnsupported
	}
	return exitOK
}

func parseZone(s string) (led.Zone, error) {
	switch s {
	case "power":
		return led.Power, nil
	case "hd0":
		return led.HD0, nil
	case "hd1":
		return led.HD1, nil
	case "hd2":
		return led.HD2, nil
	case "hd3":
		return led.HD3, nil
	default:
		return 0, fmt.Errorf("unknown led zone %q", s)
	}
}

func parseColor(s string) (led.Color, error) {
	switch s {
	case "red":
		return led.Red, nil
	case "blue":
		return led.Blue, nil
	default:
		return 0, fmt.Errorf("unknown led color %q", s)
	}
}

func parseState(s string) (led.State, error) {
	switch s {
	case "off":
		return led.Off, nil
	case "on":
		return led.On, nil
	case "blinking", "blink":
		return led.Blinking, nil
	default:
		return 0, fmt.Errorf("unknown led state %q", s)
	}
}
