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

const version = "1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

// pendingAction is one <mode flag> + its own fixed-arity arguments,
// in the order it appeared on the command line.
type pendingAction struct {
	mode string // "led", "fan", "rtc", "display", "temp"
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
	case "-t", "--temp":
		return "temp"
	default:
		return ""
	}
}

// arityFor is how many of the following command-line tokens a mode
// flag consumes as its own arguments, verbatim, regardless of what
// they look like (so e.g. a display text starting with "-" is never
// mistaken for a flag). led's arity depends on the board: EX2U's
// status LED takes one STATE word, EX2/EX4's zone/color/state scheme
// takes three. temp takes no arguments at all - it just requests and
// prints whatever raw reply comes back (see buildTempAction).
func arityFor(mode string, b board.Board) int {
	switch mode {
	case "fan", "display", "rtc":
		return 1
	case "led":
		if b == board.EX2U {
			return 1
		}
		return 3
	case "temp":
		return 0
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

	// setBoard rejects a second -b/--board that resolves to a
	// DIFFERENT board outright, rather than silently letting the last
	// one win for every action in the command (including ones a user
	// wrote right after an earlier, different -b, expecting THAT one
	// to apply - confirmed confusing in practice: `-b ex2u -f 50 -b
	// ex4 -f 2` used to apply ex4 to both actions, reporting a
	// baffling "unsupported fan level 50" for a value that is
	// perfectly valid on ex2u). Repeating the same board is harmless
	// and allowed - including via a DIFFERENT alias of it (`-b ex2u
	// -b ex2-ultra` is "the same value" semantically even though the
	// two strings differ byte-for-byte, so this compares the parsed
	// board.Board, never the raw strings).
	setBoard := func(flag, value string) error {
		parsed, err := board.Parse(value)
		if err != nil {
			return err
		}
		if opts.board != "" && opts.boardValue != parsed {
			return fmt.Errorf("%s=%s conflicts with an earlier -b/--board=%s in the same command", flag, value, opts.board)
		}
		opts.board = value
		opts.boardValue = parsed
		return nil
	}

	// hintBoard is a quick, best-effort, NEVER-ERRORING scan - the
	// same shape this whole pre-pass used to be before the fix below
	// - used only to resolve -l/--led's board-dependent arity (1 on
	// EX2U, 3 otherwise) ahead of time, for the one case that matters:
	// -l appearing before any REAL -b/--board in the command, with
	// nothing else yet known about which board it'll turn out to be.
	// It is not authoritative (it can't be - this chicken-and-egg case
	// is exactly why the real pass below exists) and its own mistakes
	// (e.g. picking up a stray "-b"-looking value from another
	// action's own argument) are harmless: it is consulted ONLY as a
	// last-resort hint, and only to size one skip, never to set
	// opts.board/opts.boardValue themselves.
	//
	// Known, accepted residual edge case (verified by hand, not
	// guessed): if led's OWN zone/state argument on EX2U is ALSO
	// deliberately "-b" (e.g. `-l -b -b ex2u`), this hint can itself
	// get confused by that same collision and still misjudge the
	// arity, which can in turn make a genuine, later -b/--board go
	// unseen by the real pass too. The failure mode is a loud, clear
	// "-b/--board is required" usage error (never silent corruption
	// or a crash), and it requires deliberately using a reserved-
	// looking string as led's own argument on top of the exact
	// ordering this already-narrow case depends on - not worth
	// chasing further for this CLI's actual argument vocabulary
	// (zone/color/state names, never "-b"-shaped in practice).
	// Keeps the FIRST successfully-parsed value, never a later one -
	// matching setBoard's own "first occurrence wins, a later
	// DIFFERENT one is a conflict" semantics below. Found the hard
	// way: keeping the LAST value instead (as an earlier version of
	// this hint did) could make `-l on -b ex2u -b ex4` compute the
	// WRONG arity for -l (sized for ex4, not the genuinely-first
	// ex2u), which then swallowed the real "-b ex2u" into led's own
	// (mis-sized) argument span - so the real pass below never even
	// saw it as a board flag, silently losing the conflict check
	// entirely and failing with a confusing "unknown led zone" error
	// instead of a clear "conflicts with an earlier -b" one.
	var hintBoard board.Board
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "-b" || a == "--board") && i+1 < len(args) {
			if b, err := board.Parse(args[i+1]); err == nil && hintBoard == board.Unknown {
				hintBoard = b
			}
			i++
		} else if strings.HasPrefix(a, "--board=") {
			if b, err := board.Parse(strings.TrimPrefix(a, "--board=")); err == nil && hintBoard == board.Unknown {
				hintBoard = b
			}
		}
	}

	// The real, authoritative -b/--board scan. Must skip over every
	// OTHER mode flag's own fixed-arity argument(s) while doing so -
	// found the hard way: `-b ex4 -d -b` (display text that happens to
	// BE "-b") used to fail with "-b requires a value", because an
	// earlier version of this pass didn't know the second "-b" token
	// was --display's own argument, not a second board flag.
	// -f/-d/-r/--serial always take exactly 1 argument regardless of
	// board, so those are always skipped correctly here. -l/--led's
	// arity is the one genuinely ambiguous case if board isn't known
	// yet AT THIS POINT in this very scan (i.e. -l appears before any
	// real -b) - that's exactly what hintBoard above is for; once a
	// real -b/--board has actually been seen earlier in THIS scan,
	// opts.boardValue (not the hint) is authoritative and used
	// instead, the same way order-independence already works for
	// every other purpose in this parser.
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-b" || a == "--board":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("%s requires a value", a)
			}
			if err := setBoard(a, args[i]); err != nil {
				return nil, err
			}
		case strings.HasPrefix(a, "--board="):
			if err := setBoard("--board", strings.TrimPrefix(a, "--board=")); err != nil {
				return nil, err
			}
		case a == "-f" || a == "--fan" || a == "-d" || a == "--display" || a == "-r" || a == "--rtc" || a == "--serial":
			i++ // skip this flag's own single argument, whatever it looks like
		case a == "-l" || a == "--led":
			b := opts.boardValue
			if opts.board == "" {
				b = hintBoard
			}
			n := 3
			if b == board.EX2U {
				n = 1
			}
			i += n
		}
	}

	// opts.boardValue was already resolved (and validated) by setBoard
	// above, for every genuine -b/--board occurrence - nothing left
	// to parse here.

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
		fmt.Fprintln(os.Stderr, "wd-control-tool: at least one of -l/--led, -f/--fan, -r/--rtc, -d/--display, -t/--temp is required")
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
	fmt.Fprint(w, `wd-control-tool -b BOARD [-n] [-D] [--serial PATH] <ACTION>...
wd-control-tool -h | --help
wd-control-tool -v | --version

-b, --board BOARD   required: ex2u, ex2, or ex4 (ex2u aliases: ex2-ultra, ex2ultra).
                    May appear anywhere on the command line and applies to every
                    action in the command. Repeating it with a DIFFERENT value is
                    a usage error (repeating the SAME value, or an alias of it, is
                    harmless).

Each ACTION is a mode flag followed directly by its own fixed number
of arguments - no delimiter is needed, and several actions can be
chained in one command, run in the order given:
  -l, --led ...       led command (see below) - ex2u, ex2, ex4
  -f, --fan LEVEL     board-native units, NOT 0-100 - see README.md - ex2u, ex2, ex4
  -r, --rtc write      push the host's current time to the MCU RTC; no read-back - ex4 only
  -d, --display TEXT   write text to the front-panel LCD, taken verbatim - ex4 only
  -t, --temp           request temperature and print the raw, undecoded MCU reply (see SPEC.md) - ex2u only, no arguments

led's own arguments:
  ex2, ex4:  ZONE COLOR STATE   zones: power,hd0,hd1[,hd2,hd3]; colors: red,blue; states: on,off,blinking(blink)
  ex2u:      STATE              states: on,off,blinking(blink),breathing(breathe)

Modifiers (may appear anywhere, apply to every action in the command):
  -n, --dry-run   print what would be done, touch no hardware; exits 0 even if the real run would fail
  -D, --debug     print extra diagnostics (frame bytes / register addresses) alongside normal output; changes nothing about what is done
  --serial PATH   MCU serial device (default /dev/ttyS1 - confirmed on real hardware for EX2/EX4 and EX2U)

Examples:
  wd-control-tool -b ex2 -f 3
  wd-control-tool -b ex4 --display "Booting..."
  wd-control-tool -b ex2 --led power blue on -f 1

Exit codes: 0 ok, 1 usage/argument error, 2 not supported on this board, 3 hardware I/O error.

Full reference, including per-board hardware details: README.md and SPEC.md.
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
	case "temp":
		return buildTempAction(b, serialPath, args)
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
		// now is captured ONCE and reused by apply below - not
		// recomputed via a second time.Now() call at send time. The
		// describe/-D output above promises "the exact frame bytes
		// that would go out the UART" (see README); a second,
		// independent time.Now() inside apply would make the ACTUAL
		// frame sent on a real run differ from the one just shown
		// (different BCD seconds/minutes, possibly more under the
		// pacing delay of any action chained before this one),
		// quietly breaking that promise.
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
				return rtc.EX4Writer{MCU: mcu.NewEX4(p)}.Set(now)
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

// ex2uTempReplyTimeout is how long buildTempAction waits for a reply
// after sending the tmp_get request before reporting "no reply" -
// generous relative to the EX2U UART's own byte timing (a handful of
// milliseconds per frame, per SPEC.md), but no real reply has ever
// been captured to calibrate against, so this is deliberately
// conservative rather than tuned to an assumption.
const ex2uTempReplyTimeout = 2 * time.Second

// buildTempAction sends EX2U's tmp_get request and prints whatever
// raw bytes come back, unconverted - see mcu.EX2U.TempReply's doc
// comment for why this doesn't attempt a Celsius/Fahrenheit decode.
// EX2U-only: no temp command is documented for EX2/EX4's legacy
// protocol (see SPEC.md's command tables).
func buildTempAction(b board.Board, serialPath string, args []string) (*action, int) {
	if b != board.EX2U {
		return unsupportedErr("temp is not supported on board %v", b)
	}
	frame := mcu.EX2UTempRequestFrame()
	return &action{
		describe:   fmt.Sprintf("request temperature from EX2U MCU (%s) and print the raw reply (undecoded - see SPEC.md)", serialPath),
		debugLines: frameDebugLines(nil, frame, serialPath, 19200),
		apply: func() error {
			p, err := mcu.Open(serialPath, 19200)
			if err != nil {
				return err
			}
			defer p.Close()
			reply, err := mcu.NewEX2U(p).TempReply(ex2uTempReplyTimeout)
			if err != nil {
				return err
			}
			if len(reply) == 0 {
				fmt.Printf("temp: no reply from MCU within %v (reply framing is undecoded - see SPEC.md's Unresolved #1)\n", ex2uTempReplyTimeout)
				return nil
			}
			fmt.Printf("temp: raw reply (undecoded - see SPEC.md's Unresolved #1): % X\n", reply)
			return nil
		},
	}, exitOK
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
