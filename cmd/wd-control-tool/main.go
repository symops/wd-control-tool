// wd-control-tool is a self-contained replacement for the proprietary
// fan_control/rtc/led/up_send_ctl (EX2/EX4) and mcu_ctl (EX2U) tools -
// no xmldb, no up_read_daemon/up_send_daemon, no vendor .so stack.
// See SPEC.md for the full per-board hardware reference and README.md
// for the CLI reference this file implements.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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

const version = "1.0.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

// options is the result of parsing argv - see parseArgs.
type options struct {
	board        string
	mode         string // "led", "fan", "rtc", "display", or "" if none selected
	modeCount    int
	commandGiven bool
	cmdArgs      []string
	dryRun       bool
	debug        bool
	serial       string
	help         bool
	version      bool
}

// parseArgs is a small hand-rolled parser, not the stdlib flag
// package: -c/--command's job is to mark "everything after this is
// the selected command's own positional argument list", which
// flag.FlagSet has no way to express (the same reason verona-tool
// needed its own custom parser for its -k/-t/-f shortcuts - see its
// SPEC.md). Every other flag may appear in any order before -c;
// nothing after -c is parsed as a flag at all.
func parseArgs(args []string) (*options, error) {
	opts := &options{serial: "/dev/ttyS1"}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			opts.help = true
		case a == "-v" || a == "--version":
			opts.version = true
		case a == "-b" || a == "--board":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("%s requires a value", a)
			}
			opts.board = args[i]
		case strings.HasPrefix(a, "--board="):
			opts.board = strings.TrimPrefix(a, "--board=")
		case a == "-l" || a == "--led":
			opts.mode, opts.modeCount = "led", opts.modeCount+1
		case a == "-f" || a == "--fan":
			opts.mode, opts.modeCount = "fan", opts.modeCount+1
		case a == "-r" || a == "--rtc":
			opts.mode, opts.modeCount = "rtc", opts.modeCount+1
		case a == "-d" || a == "--display":
			opts.mode, opts.modeCount = "display", opts.modeCount+1
		case a == "-n" || a == "--dry-run":
			opts.dryRun = true
		case a == "-D" || a == "--debug":
			opts.debug = true
		case a == "--serial":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--serial requires a value")
			}
			opts.serial = args[i]
		case strings.HasPrefix(a, "--serial="):
			opts.serial = strings.TrimPrefix(a, "--serial=")
		case a == "-c" || a == "--command":
			opts.commandGiven = true
			opts.cmdArgs = args[i+1:]
			i = len(args) // nothing after -c/--command is parsed as a flag
		default:
			return nil, fmt.Errorf("unrecognized argument %q", a)
		}
	}

	if opts.modeCount > 1 {
		return nil, fmt.Errorf("only one of -l/-f/-r/-d may be given")
	}
	return opts, nil
}

func run(args []string) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitUsage
	}

	if opts.help {
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "wd-control-tool: -h/--help must be used alone")
			return exitUsage
		}
		printUsage(os.Stdout)
		return exitOK
	}
	if opts.version {
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "wd-control-tool: -v/--version must be used alone")
			return exitUsage
		}
		fmt.Printf("wd-control-tool %s\nSymo <symops@gmail.com>\n", version)
		return exitOK
	}

	if opts.board == "" {
		fmt.Fprintln(os.Stderr, "wd-control-tool: -b/--board is required")
		return exitUsage
	}
	b, err := board.Parse(opts.board)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitUsage
	}

	if opts.mode == "" {
		fmt.Fprintln(os.Stderr, "wd-control-tool: one of -l/--led, -f/--fan, -r/--rtc, -d/--display is required")
		printUsage(os.Stderr)
		return exitUsage
	}
	if !opts.commandGiven {
		fmt.Fprintf(os.Stderr, "wd-control-tool: -c/--command is required with -%c/--%s\n", opts.mode[0], opts.mode)
		return exitUsage
	}

	act, code := buildAction(b, opts.mode, opts.serial, opts.cmdArgs)
	if act == nil {
		return code // a usage error building the action itself (e.g. wrong arg count)
	}
	return runAction(act, opts.dryRun, opts.debug)
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `wd-control-tool -b BOARD <-l|-f|-r|-d> [-n] [-D] -c ARG...
wd-control-tool -h | --help
wd-control-tool -v | --version

-c/--command must come LAST: everything after it is taken verbatim as
the selected command's own argument list and is never parsed as a
flag, even if it looks like one. Put -n/-D (and -b and the mode flag)
before -c, not after.

Boards (-b/--board): ex2u, ex2, ex4

Exactly one of these selects what -c's arguments mean:
  -l, --led       led command (see below)
  -f, --fan       fan LEVEL (board-native units - see README.md)
  -r, --rtc       rtc write (ex4 only for now - see SPEC.md)
  -d, --display   display TEXT (ex4 only)

led's own arguments (after -c):
  ex2, ex4:  ZONE COLOR STATE   zones: power,hd0,hd1[,hd2,hd3]; colors: red,blue; states: on,off,blinking
  ex2u:      STATE              states: on,off,blinking,breathing

Modifiers (before -c):
  -n, --dry-run   print what would be done, touch no hardware; combines with everything, exits 0 even if the real run would fail
  -D, --debug     print extra diagnostics (frame bytes / register addresses) alongside normal output; changes nothing about what is done
  --serial PATH   MCU serial device (default /dev/ttyS1 - confirmed on EX2/EX4; EX2U's uart1 tty node is NOT yet confirmed)

Examples:
  wd-control-tool -b ex4 --led -c power blue on
  wd-control-tool -b ex2u -f -n -D -c 50
  wd-control-tool -b ex4 -r -c write
  wd-control-tool -b ex4 -d -c "Booting..."

Exit codes: 0 ok, 1 usage/argument error, 2 not supported on this board, 3 hardware I/O error.
`)
}

// action is a planned command: describe/debugLines are always pure
// (no I/O, safe under -n); apply performs the real hardware access
// and is only called for a real (non-dry-run) run.
type action struct {
	describe   string
	debugLines []string
	planErr    error // non-nil: the command's arguments were well-formed in count, but invalid in value (bad level/zone/color/state) - this is what -n reports as "would fail"
	apply      func() error
}

func runAction(a *action, dryRun, debug bool) int {
	if debug {
		fmt.Println("debug:", a.describe)
		for _, l := range a.debugLines {
			fmt.Println("debug:", l)
		}
	}

	if a.planErr != nil {
		if dryRun {
			fmt.Println("would fail:", a.planErr)
			return exitOK
		}
		fmt.Fprintln(os.Stderr, "wd-control-tool:", a.planErr)
		return exitUsage
	}

	if dryRun {
		fmt.Println("would", a.describe)
		return exitOK
	}

	if err := a.apply(); err != nil {
		fmt.Fprintln(os.Stderr, "wd-control-tool:", err)
		return exitHardwareIO
	}
	return exitOK
}

// buildAction dispatches to the per-mode builder. Returns (nil, code)
// for a hard usage error (wrong argument count - not something -n
// should neutralize, since there is no well-formed command to
// simulate at all).
func buildAction(b board.Board, mode, serialPath string, args []string) (*action, int) {
	switch mode {
	case "fan":
		return buildFanAction(b, serialPath, args)
	case "led":
		return buildLEDAction(b, serialPath, args)
	case "display":
		return buildDisplayAction(b, serialPath, args)
	case "rtc":
		return buildRTCAction(b, serialPath, args)
	default:
		panic("unreachable mode " + mode)
	}
}

func usageErr(format string, a ...any) (*action, int) {
	fmt.Fprintf(os.Stderr, "wd-control-tool: "+format+"\n", a...)
	return nil, exitUsage
}

func unsupportedErr(format string, a ...any) (*action, int) {
	fmt.Fprintf(os.Stderr, "wd-control-tool: "+format+"\n", a...)
	return nil, exitUnsupported
}

func buildFanAction(b board.Board, serialPath string, args []string) (*action, int) {
	if len(args) != 1 {
		return usageErr("usage: -b BOARD -f -c LEVEL")
	}
	level, err := strconv.Atoi(args[0])
	if err != nil {
		return usageErr("fan level must be an integer: %v", err)
	}

	switch b {
	case board.EX2U:
		frame, ferr := mcu.EX2UFanPercentFrame(level)
		return &action{
			describe:   fmt.Sprintf("send fan %d%% to EX2U MCU (%s)", level, serialPath),
			debugLines: frameDebugLines(ferr, frame, serialPath, 19200),
			planErr:    ferr,
			apply: func() error {
				p, err := mcu.Open(serialPath, 19200)
				if err != nil {
					return err
				}
				defer p.Close()
				return mcu.NewEX2U(p).FanPercent(level)
			},
		}, exitOK
	case board.EX4:
		frame, ferr := mcu.EX4FanLevelFrame(level)
		return &action{
			describe:   fmt.Sprintf("send fan level %d to EX4 MCU (%s)", level, serialPath),
			debugLines: frameDebugLines(ferr, frame, serialPath, 19200),
			planErr:    ferr,
			apply: func() error {
				p, err := mcu.Open(serialPath, 19200)
				if err != nil {
					return err
				}
				defer p.Close()
				return mcu.NewEX4(p).FanLevel(level)
			},
		}, exitOK
	case board.EX2:
		ws, perr := fan.Ex2FanPlan(level)
		return &action{
			describe:   fmt.Sprintf("set EX2 fan to level %d via /dev/mem", level),
			debugLines: regWriteDebugLines(perr, ws),
			planErr:    perr,
			apply: func() error {
				m, err := regio.Open()
				if err != nil {
					return err
				}
				defer m.Close()
				return m.ApplyAll(ws)
			},
		}, exitOK
	default:
		return unsupportedErr("fan is not supported on board %v", b)
	}
}

func buildLEDAction(b board.Board, serialPath string, args []string) (*action, int) {
	if b == board.EX2U {
		if len(args) != 1 {
			return usageErr("usage: -b ex2u -l -c STATE   (STATE: %s)", strings.Join(mcu.EX2ULEDStates, ", "))
		}
		state := args[0]
		frame, ferr := mcu.EX2ULEDFrame(state)
		return &action{
			describe:   fmt.Sprintf("send led %s to EX2U MCU (%s)", state, serialPath),
			debugLines: frameDebugLines(ferr, frame, serialPath, 19200),
			planErr:    ferr,
			apply: func() error {
				p, err := mcu.Open(serialPath, 19200)
				if err != nil {
					return err
				}
				defer p.Close()
				switch state {
				case "on":
					return mcu.NewEX2U(p).LEDOn()
				case "off":
					return mcu.NewEX2U(p).LEDOff()
				case "blinking", "blink":
					return mcu.NewEX2U(p).LEDBlink()
				default: // "breathing", "breathe"
					return mcu.NewEX2U(p).LEDBreathe()
				}
			},
		}, exitOK
	}

	if b != board.EX2 && b != board.EX4 {
		return unsupportedErr("led is not supported on board %v", b)
	}
	if len(args) != 3 {
		return usageErr("usage: -b BOARD -l -c ZONE COLOR STATE")
	}

	zone, err := parseZone(args[0])
	if err == nil && (zone == led.HD2 || zone == led.HD3) && b == board.EX2 {
		err = fmt.Errorf("EX2 only has hd0/hd1, not %q", args[0])
	}
	color, cerr := parseColor(args[1])
	stateVal, serr := parseState(args[2])
	if err == nil {
		err = cerr
	}
	if err == nil {
		err = serr
	}

	var ws []regio.Write
	if err == nil {
		ws, err = led.Plan(zone, color, stateVal)
	}

	return &action{
		describe:   fmt.Sprintf("set %s %v %v %v via /dev/mem", b, args[0], args[1], args[2]),
		debugLines: regWriteDebugLines(err, ws),
		planErr:    err,
		apply: func() error {
			m, oerr := regio.Open()
			if oerr != nil {
				return oerr
			}
			defer m.Close()
			return m.ApplyAll(ws)
		},
	}, exitOK
}

func buildDisplayAction(b board.Board, serialPath string, args []string) (*action, int) {
	if !b.HasDisplay() {
		return unsupportedErr("board %v has no display", b)
	}
	if len(args) != 1 {
		return usageErr("usage: -b ex4 -d -c TEXT")
	}
	text := args[0]
	frame := mcu.EX4DisplayFrame(text)
	return &action{
		describe:   fmt.Sprintf("send display text %q to EX4 MCU (%s)", text, serialPath),
		debugLines: frameDebugLines(nil, frame, serialPath, 19200),
		apply: func() error {
			p, err := mcu.Open(serialPath, 19200)
			if err != nil {
				return err
			}
			defer p.Close()
			return mcu.NewEX4(p).Display(text)
		},
	}, exitOK
}

func buildRTCAction(b board.Board, serialPath string, args []string) (*action, int) {
	if len(args) != 1 || args[0] != "write" {
		return usageErr("usage: -b BOARD -r -c write")
	}

	switch b {
	case board.EX4:
		now := time.Now()
		year, mon, dom := now.Date()
		dow := int(now.Weekday())
		frame := mcu.EX4DateTimeFrame(year, int(mon), dom, dow, now.Hour(), now.Minute(), now.Second())
		return &action{
			describe:   fmt.Sprintf("write current time (%s) to EX4 MCU RTC (%s)", now.Format(time.RFC3339), serialPath),
			debugLines: frameDebugLines(nil, frame, serialPath, 19200),
			apply: func() error {
				p, err := mcu.Open(serialPath, 19200)
				if err != nil {
					return err
				}
				defer p.Close()
				return rtc.EX4Writer{MCU: mcu.NewEX4(p)}.Set(time.Now())
			},
		}, exitOK
	case board.EX2:
		return &action{
			describe: "write current time to EX2 MCU RTC",
			planErr:  rtc.ErrEX2NotImplemented,
		}, exitOK
	default:
		return unsupportedErr("rtc is not supported on board %v", b)
	}
}

func frameDebugLines(planErr error, frame []byte, serialPath string, baud int) []string {
	if planErr != nil {
		return nil
	}
	return []string{
		fmt.Sprintf("serial=%s baud=%d", serialPath, baud),
		fmt.Sprintf("frame=% X", frame),
	}
}

func regWriteDebugLines(planErr error, ws []regio.Write) []string {
	if planErr != nil {
		return nil
	}
	lines := make([]string, 0, len(ws))
	for _, w := range ws {
		lines = append(lines, fmt.Sprintf("/dev/mem 0x%x: set=0x%08x clear=0x%08x", w.Addr, w.Set, w.Clear))
	}
	return lines
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
