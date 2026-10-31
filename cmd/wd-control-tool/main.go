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

// pendingAction is one <mode flag> + its own fixed-arity arguments,
// in the order it appeared on the command line.
type pendingAction struct {
	mode string // "led", "fan", "rtc", "display"
	args []string
}

// options is the result of parsing argv - see parseArgs.
type options struct {
	board      string
	boardValue board.Board // resolved once in parseArgs' first pass; run() reuses it instead of re-parsing opts.board (board.Parse(opts.board) can't fail a second time)
	actions    []pendingAction
	dryRun     bool
	debug      bool
	serial     string
	help       bool
	version    bool
}

func modeNameFor(flag string) string {
	switch flag {
	case "-l", "--led":
		return "led"
	case "-f", "--fan":
		return "fan"
	case "-r", "--rtc":
		return "rtc"
	case "-d", "--display":
		return "display"
	default:
		return ""
	}
}

// arityFor is how many of the following command-line tokens a mode
// flag consumes as its own arguments, verbatim, regardless of what
// they look like (so e.g. a display text starting with "-" is never
// mistaken for a flag). led's arity depends on the board: EX2U's
// status LED takes one STATE word, EX2/EX4's zone/color/state scheme
// takes three.
func arityFor(mode string, b board.Board) int {
	switch mode {
	case "fan", "display", "rtc":
		return 1
	case "led":
		if b == board.EX2U {
			return 1
		}
		return 3
	default:
		return 0
	}
}

// parseArgs is a small hand-rolled parser, not the stdlib flag
// package: each mode flag (-l/-f/-r/-d) consumes a fixed number of
// the following tokens as its own argument list (see arityFor), so
// several actions can be chained in one invocation and no delimiter
// flag is needed between a mode flag and its arguments, or between
// one action and the next. -b/--board is looked up in a first pass
// (it may appear anywhere) because led's arity depends on it.
func parseArgs(args []string) (*options, error) {
	opts := &options{serial: "/dev/ttyS1"}

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-b" || a == "--board" {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("%s requires a value", a)
			}
			opts.board = args[i]
		} else if strings.HasPrefix(a, "--board=") {
			opts.board = strings.TrimPrefix(a, "--board=")
		}
	}

	if opts.board != "" {
		parsed, err := board.Parse(opts.board)
		if err != nil {
			return nil, err
		}
		opts.boardValue = parsed
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			opts.help = true
		case a == "-v" || a == "--version":
			opts.version = true
		case a == "-b" || a == "--board":
			i++ // value already captured above
		case strings.HasPrefix(a, "--board="):
			// already captured above
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
		case modeNameFor(a) != "":
			if opts.board == "" {
				return nil, fmt.Errorf("-b/--board is required (to use %s)", a)
			}
			mode := modeNameFor(a)
			n := arityFor(mode, opts.boardValue)
			start, end := i+1, i+1+n
			if end > len(args) {
				return nil, fmt.Errorf("%s requires %d argument(s)", a, n)
			}
			opts.actions = append(opts.actions, pendingAction{mode: mode, args: args[start:end]})
			i = end - 1
		default:
			return nil, fmt.Errorf("unrecognized argument %q", a)
		}
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
	// opts.boardValue was already resolved (and validated) by
	// parseArgs - board.Parse(opts.board) cannot fail again here.
	b := opts.boardValue

	if len(opts.actions) == 0 {
		fmt.Fprintln(os.Stderr, "wd-control-tool: at least one of -l/--led, -f/--fan, -r/--rtc, -d/--display is required")
		printUsage(os.Stderr)
		return exitUsage
	}

	// Actions run in the order they appeared on the command line.
	// Under a real (non-dry-run) run, the first one that fails stops
	// the rest - runAction only ever returns a non-exitOK code in
	// that case, since under -n it always reports and returns 0.
	for _, pa := range opts.actions {
		act, code := buildAction(b, pa.mode, opts.serial, pa.args)
		if act == nil {
			return code
		}
		if code := runAction(act, opts.dryRun, opts.debug); code != exitOK {
			return code
		}
	}
	return exitOK
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `wd-control-tool -b BOARD [-n] [-D] <ACTION>...
wd-control-tool -h | --help
wd-control-tool -v | --version

Each ACTION is a mode flag followed directly by its own fixed number
of arguments - no delimiter is needed, and several actions can be
chained in one command, run in the order given:
  -l, --led ...       led command (see below)
  -f, --fan LEVEL     board-native units - see README.md
  -r, --rtc write      ex4 only for now - see SPEC.md
  -d, --display TEXT   ex4 only

led's own arguments:
  ex2, ex4:  ZONE COLOR STATE   zones: power,hd0,hd1[,hd2,hd3]; colors: red,blue; states: on,off,blinking
  ex2u:      STATE              states: on,off,blinking,breathing

Modifiers (may appear anywhere, apply to every action in the command):
  -n, --dry-run   print what would be done, touch no hardware; exits 0 even if the real run would fail
  -D, --debug     print extra diagnostics (frame bytes / register addresses) alongside normal output; changes nothing about what is done
  --serial PATH   MCU serial device (default /dev/ttyS1 - confirmed on EX2/EX4; EX2U's uart1 tty node is NOT yet confirmed)

Examples:
  wd-control-tool -b ex2 -f 3
  wd-control-tool -b ex4 --display "Booting..."
  wd-control-tool -b ex2 --led power blue on -f 1

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
// for a hard usage error. Argument COUNT is already guaranteed
// correct by parseArgs' fixed arity - any remaining validation here
// (unknown zone/color/state names, level out of range, "rtc"'s only
// recognized subcommand) is a value problem, reported via
// action.planErr so -n can still describe it as "would fail".
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

func unsupportedErr(format string, a ...any) (*action, int) {
	fmt.Fprintf(os.Stderr, "wd-control-tool: "+format+"\n", a...)
	return nil, exitUnsupported
}

func buildFanAction(b board.Board, serialPath string, args []string) (*action, int) {
	level, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "wd-control-tool: fan level must be an integer: %v\n", err)
		return nil, exitUsage
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
	text := args[0]
	frame, ferr := mcu.EX4DisplayFrame(text)
	return &action{
		describe:   fmt.Sprintf("send display text %q to EX4 MCU (%s)", text, serialPath),
		debugLines: frameDebugLines(ferr, frame, serialPath, 19200),
		planErr:    ferr,
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
	if args[0] != "write" {
		fmt.Fprintf(os.Stderr, "wd-control-tool: unknown rtc subcommand %q (only \"write\" is supported)\n", args[0])
		return nil, exitUsage
	}

	switch b {
	case board.EX4:
		now := time.Now()
		year, mon, dom := now.Date()
		dow := int(now.Weekday())
		frame, ferr := mcu.EX4DateTimeFrame(year, int(mon), dom, dow, now.Hour(), now.Minute(), now.Second())
		return &action{
			describe:   fmt.Sprintf("write current time (%s) to EX4 MCU RTC (%s)", now.Format(time.RFC3339), serialPath),
			debugLines: frameDebugLines(ferr, frame, serialPath, 19200),
			planErr:    ferr,
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
