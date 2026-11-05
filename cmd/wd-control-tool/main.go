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
	mode string // "led", "fan", "rtc", "display", "temp", "sleep"
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
	case "-s", "--sleep":
		return "sleep"
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
// prints the decoded temperature (see buildTempAction).
func arityFor(mode string, b board.Board) int {
	switch mode {
	case "fan", "display", "rtc", "sleep":
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
// package: each mode flag (-l/-f/-r/-d/-t/-s) consumes a fixed number
// of the following tokens as its own argument list (see arityFor), so
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
		case a == "-f" || a == "--fan" || a == "-d" || a == "--display" || a == "-r" || a == "--rtc" || a == "-s" || a == "--sleep" || a == "--serial":
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
		fmt.Fprintln(os.Stderr, "wd-control-tool: at least one of -l/--led, -f/--fan, -r/--rtc, -d/--display, -t/--temp, -s/--sleep is required")
		printUsage(os.Stderr)
		return exitUsage
	}

	// Actions run in the order they appeared on the command line.
	// Under a real (non-dry-run) run, the first one that fails stops
	// the rest - runAction only ever returns a non-exitOK code in
	// that case, since under -n it always reports and returns 0.
	for _, pa := range opts.actions {
		act, code := buildAction(b, pa.mode, opts.serial, pa.args, opts.debug)
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
  -f, --fan rpm       read back the current fan RPM instead of setting a level - ex2 only
  -r, --rtc w|r|s      w: push the host's current time to the MCU RTC - ex2, ex4
                    r: read the MCU RTC and print it - ex2, ex4
                    s: like r, and also set the host's system clock from it - ex2, ex4
  -d, --display TEXT   write text to the front-panel LCD, taken verbatim (Cyrillic is
                    auto-transliterated to Latin - the LCD's font ROM has none) - ex4 only
  -t, --temp           request and print the temperature as "Temperature: N C" (hardware-confirmed - see SPEC.md) - ex2u only, no arguments
  -s, --sleep on|off   put the front-panel LCD to sleep (dark) / wake it back up - ex4 only

led's own arguments:
  ex2:       ZONE COLOR STATE   zones: power,hd0,hd1; colors: red,blue; states: on,off,blinking(blink)
                    blinking is confirmed for power (both colors) and hd0/hd1 red;
                    hd0/hd1 blue blinking has no confirmed register behavior and
                    reports an error rather than assuming it matches red
  ex4:       ZONE COLOR STATE   zones: power,hd0,hd1,hd2,hd3; colors: red,blue; states: on,off,blinking(blink)
                    blue blinking on hd0..hd3 has no vendor implementation at all
                    (confirmed by decompilation) and reports an error; every other
                    zone/color/state combination is hardware-confirmed - see SPEC.md
  ex2u:      STATE              states: on,off,blinking(blink),breathing(breathe)

Modifiers (may appear anywhere, apply to every action in the command):
  -n, --dry-run   print what would be done, touch no hardware; exits 0 even if the real run would fail
  -D, --debug     print extra diagnostics (frame bytes / register addresses) alongside normal output; changes nothing about what is done
  --serial PATH   MCU serial device (default /dev/ttyS1 - confirmed on real hardware for EX2/EX4 and EX2U)

Examples:
  wd-control-tool -b ex2 -f 3
  wd-control-tool -b ex4 --display "Booting..."
  wd-control-tool -b ex2 --led power blue on -f 1
  wd-control-tool -b ex4 --sleep on

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
// (unknown zone/color/state names, level out of range, "rtc"'s
// recognized subcommands) is a value problem, reported via
// action.planErr so -n can still describe it as "would fail".
func buildAction(b board.Board, mode, serialPath string, args []string, debug bool) (*action, int) {
	switch mode {
	case "fan":
		// "-f rpm" is a special argument value, not a level: read the
		// current fan RPM instead of setting a speed. EX2-only - see
		// buildEX2FanRPMAction.
		if args[0] == "rpm" {
			switch b {
			case board.EX2:
				return buildEX2FanRPMAction(serialPath, debug)
			case board.EX4:
				return buildEX4FanRPMAction()
			default:
				return unsupportedErr("fan rpm read is not supported on board %v", b)
			}
		}
		return buildFanAction(b, serialPath, args)
	case "led":
		return buildLEDAction(b, serialPath, args)
	case "display":
		return buildDisplayAction(b, serialPath, args)
	case "rtc":
		return buildRTCAction(b, serialPath, args, debug)
	case "temp":
		return buildTempAction(b, serialPath, args, debug)
	case "sleep":
		return buildSleepAction(b, serialPath, args)
	default:
		panic("unreachable mode " + mode)
	}
}

// buildSleepAction wraps EX4's LCDSleepMode - hardware-confirmed to
// put the front-panel LCD to sleep (dark)/wake it back up, see
// mcu.EX4LCDSleepFrame's doc comment for the value convention.
func buildSleepAction(b board.Board, serialPath string, args []string) (*action, int) {
	if b != board.EX4 {
		return unsupportedErr("sleep is not supported on board %v", b)
	}
	var sleep bool
	var err error
	switch args[0] {
	case "on":
		sleep = true
	case "off":
		sleep = false
	default:
		err = fmt.Errorf("sleep state must be \"on\" or \"off\", got %q", args[0])
	}

	// mcu.EX4LCDSleepFrame itself never errors (sleep is a plain bool)
	// - frameDebugLines already ignores frame entirely when err != nil,
	// so computing it unconditionally here is safe either way.
	frame := mcu.EX4LCDSleepFrame(sleep)
	return &action{
		describe:   fmt.Sprintf("send lcd sleep %s to EX4 MCU (%s)", args[0], serialPath),
		debugLines: frameDebugLines(err, frame, serialPath, 19200),
		planErr:    err,
		apply: func() error {
			p, operr := mcu.Open(serialPath, 19200)
			if operr != nil {
				return operr
			}
			defer p.Close()
			return mcu.NewEX4(p).LCDSleep(sleep)
		},
	}, exitOK
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

// ex2FanRPMReplyTimeout is how long buildEX2FanRPMAction waits for a
// reply after sending the FanStatusGet request - matches
// ex2uTempReplyTimeout's reasoning (generous relative to a real
// capture, where the reply arrived almost immediately).
const ex2FanRPMReplyTimeout = 2 * time.Second

// buildEX2FanRPMAction sends EX2's FanStatusGet request and prints
// the decoded RPM, matching the vendor fan_control -g's own "fan rpm
// = N" wording - see mcu.EX2DecodeFanRPM for the confirmed decode.
// EX2-only: no equivalent read has been captured for EX4/EX2U.
func buildEX2FanRPMAction(serialPath string, debug bool) (*action, int) {
	frame := mcu.EX2FanStatusRequestFrame()
	return &action{
		describe:   fmt.Sprintf("request fan RPM from EX2 MCU (%s)", serialPath),
		debugLines: frameDebugLines(nil, frame, serialPath, ex2LegacyBaud),
		apply: func() error {
			p, err := mcu.Open(serialPath, ex2LegacyBaud)
			if err != nil {
				return err
			}
			defer p.Close()
			reply, err := mcu.NewEX2(p).FanStatusReply(ex2FanRPMReplyTimeout)
			if err != nil {
				return err
			}
			if debug {
				fmt.Printf("debug: reply=% X\n", reply)
			}
			if len(reply) == 0 {
				fmt.Printf("fan: no reply from MCU within %v\n", ex2FanRPMReplyTimeout)
				return nil
			}
			rpm, derr := mcu.EX2DecodeFanRPM(reply)
			if derr != nil {
				// derr's own text embeds the raw reply bytes - not
				// printed here so -D stays the only way to see them.
				fmt.Println("fan: couldn't decode an RPM value from the MCU's reply (run with -D to see the raw bytes)")
				return nil
			}
			fmt.Printf("fan rpm = %d\n", rpm)
			return nil
		},
	}, exitOK
}

// buildEX4FanRPMAction samples EX4's fan tachometer register directly
// via /dev/mem, matching the vendor fan_control -g's own "fan rpm =
// N" wording - see fan.EX4ReadFanPulses/fan.EX4DecodeFanRPM's own doc
// comments for how this was confirmed (decompiling the real
// libshare.so, not guessed): unlike EX2, EX4's RPM read-back is not
// an MCU/UART command at all, so this takes no serialPath and never
// opens /dev/ttyS1.
func buildEX4FanRPMAction() (*action, int) {
	return &action{
		describe: fmt.Sprintf("sample EX4 fan tachometer (/dev/mem 0x%x bit %d) for %v",
			fan.EX4FanTachAddr, fan.EX4FanTachBit, fan.EX4FanTachSampleWindow),
		apply: func() error {
			m, err := regio.Open()
			if err != nil {
				return err
			}
			defer m.Close()
			pulses, err := fan.EX4ReadFanPulses(m, fan.EX4FanTachSampleWindow)
			if err != nil {
				return err
			}
			fmt.Printf("fan rpm = %d\n", fan.EX4DecodeFanRPM(pulses))
			return nil
		},
	}, exitOK
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

	// EX2's power LED is a special case: unlike every other LED zone
	// on EX2/EX4 (direct /dev/mem GPIO pokes - see pkg/led), it turned
	// out to be MCU-driven - see buildEX2PowerLEDAction.
	if err == nil && b == board.EX2 && zone == led.Power {
		return buildEX2PowerLEDAction(serialPath, args[1], args[2])
	}

	color, cerr := parseColor(args[1])
	stateVal, serr := parseState(args[2])
	if err == nil {
		err = cerr
	}
	if err == nil {
		err = serr
	}

	// EX4's HD-bay LEDs (hd0..hd3) are NOT the same mechanism as
	// EX2's - a real-hardware report showed EX2's confirmed hd-bay
	// registers have no effect on EX4, and a second report (plus a
	// decompile of the real EX4 `led` binary) found EX4's blue
	// hd-bay LEDs are driven by one register shared across all four
	// bays, while red is per-bay but in a different register pair
	// from EX2's. See pkg/led.EX4HDBayPlan and SPEC.md's "HD-bay".
	var ws []regio.Write
	if err == nil && b == board.EX4 && zone != led.Power {
		ws, err = led.EX4HDBayPlan(zone, color, stateVal)
	} else if err == nil {
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

// ex2LegacyBaud is EX2's legacy-MCU UART rate - 115200, NOT EX4's
// 19200 (confirmed, see SPEC.md's "UART line settings" - do not
// assume parity between boards).
const ex2LegacyBaud = 115200

// buildEX2PowerLEDAction handles EX2's power LED: a live strace of
// the real vendor `led` tool on real EX2 hardware showed it opens no
// /dev/mem at all for `led power ...` - only /dev/ttyS1, via the
// legacy MCU's NewPWRLed command. See mcu.EX2PowerLEDFrames for the
// confirmed byte encoding and SPEC.md's "EX2/EX4 protocol".
func buildEX2PowerLEDAction(serialPath, colorArg, stateArg string) (*action, int) {
	color, cerr := parseColor(colorArg)
	stateVal, serr := parseState(stateArg)
	err := cerr
	if err == nil {
		err = serr
	}

	var frames [2][]byte
	if err == nil {
		// stateVal.String(), not the raw stateArg: normalizes aliases
		// ("blink" -> "blinking") to exactly the spelling
		// EX2PowerLEDFrames itself switches on.
		frames, err = mcu.EX2PowerLEDFrames(color.String(), stateVal.String())
	}

	otherColor := "red"
	if color == led.Red {
		otherColor = "blue"
	}

	var debugLines []string
	if err == nil {
		debugLines = []string{
			fmt.Sprintf("serial=%s baud=%d", serialPath, ex2LegacyBaud),
			fmt.Sprintf("frame1 (clear %s)=% X", otherColor, frames[0]),
			fmt.Sprintf("frame2 (%s %s)=% X", colorArg, stateArg, frames[1]),
		}
	}

	return &action{
		describe:   fmt.Sprintf("send led power %s %s to EX2 MCU (%s)", colorArg, stateArg, serialPath),
		debugLines: debugLines,
		planErr:    err,
		apply: func() error {
			p, operr := mcu.Open(serialPath, ex2LegacyBaud)
			if operr != nil {
				return operr
			}
			defer p.Close()
			return mcu.NewEX2(p).PowerLED(color.String(), stateVal.String())
		},
	}, exitOK
}

func buildDisplayAction(b board.Board, serialPath string, args []string) (*action, int) {
	if !b.HasDisplay() {
		return unsupportedErr("board %v has no display", b)
	}
	// EX4's LCD font ROM has no Cyrillic glyphs at all - confirmed:
	// the vendor's own tool shows the same garbled output for the
	// same text, on the same hardware, so this isn't a protocol
	// encoding issue this tool could fix by sending different bytes.
	// Transliterating to Latin first is the only way to show
	// something readable for Cyrillic input - see transliterateCyrillic.
	original := args[0]
	text := transliterateCyrillic(original)
	frame, ferr := mcu.EX4DisplayFrame(text)
	debugLines := frameDebugLines(ferr, frame, serialPath, 19200)
	if ferr == nil && text != original {
		debugLines = append([]string{
			fmt.Sprintf("transliterated %q -> %q (EX4's LCD has no Cyrillic glyphs)", original, text),
		}, debugLines...)
	}
	return &action{
		describe:   fmt.Sprintf("send display text %q to EX4 MCU (%s)", text, serialPath),
		debugLines: debugLines,
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

// ex2RTCReplyTimeout is how long "rtc r"/"rtc s" wait for
// RDateAndTime's reply - matches ex2FanRPMReplyTimeout's reasoning
// (generous relative to a real capture, where the reply arrived in
// well under a second).
const ex2RTCReplyTimeout = 2 * time.Second

// ex2RTCMaxAttempts: real-hardware testing found chained rapid-fire
// "rtc r" actions (six in one command, zero think-time apart)
// getting a corrupted/undecodable reply after the first one - a
// single isolated request always worked. A fixed pre-request delay
// was tried and had ZERO measurable effect (byte-for-byte identical
// garbled output with or without it - see
// mcu.(*EX2).DateAndTimeReply's own doc comment), ruling out "the
// MCU just needs more settle time" as the fix. Retrying the whole
// request (which re-flushes and re-sends - see sendAndRecv) on a
// decode failure is more robust than tuning a magic delay against
// whatever is actually happening on the wire, without needing to
// understand or guess the exact mechanism.
const ex2RTCMaxAttempts = 3

// rtcTimeLayout matches the real vendor `rtc` tool's own printed
// format exactly (confirmed via a live capture: "RTC time =
// 2026/11/2 Mon 13:39:43") - Go's reference-time layout for
// year/month-no-zero/day-no-zero, abbreviated weekday, zero-padded
// time.
const rtcTimeLayout = "2006/1/2 Mon 15:04:05"

// buildRTCAction dispatches "-r"/"--rtc"'s one argument to the real
// vendor `rtc` tool's own letters: w (SYS -> RTC), r (RTC -> SYS,
// print only) and s (RTC -> SYS, also sets the host's system clock) -
// see README.md/SPEC.md's "RTC" section.
func buildRTCAction(b board.Board, serialPath string, args []string, debug bool) (*action, int) {
	switch args[0] {
	case "w":
		return buildRTCWriteAction(b, serialPath)
	case "r":
		return buildRTCReadAction(b, serialPath, debug, false)
	case "s":
		return buildRTCReadAction(b, serialPath, debug, true)
	default:
		fmt.Fprintf(os.Stderr, "wd-control-tool: unknown rtc subcommand %q (want w, r or s)\n", args[0])
		return nil, exitUsage
	}
}

func buildRTCWriteAction(b board.Board, serialPath string) (*action, int) {
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
	// The RTC stores UTC, not local time (confirmed on real EX2
	// hardware - see pkg/rtc's own package doc comment for how).
	// Fields come from now.UTC(), not now itself, so this preview
	// frame matches exactly what EX4Writer/EX2Writer.Set(now) below
	// actually sends (Set converts to UTC internally too) - the same
	// -D accuracy promise this function's own top comment already
	// requires.
	utc := now.UTC()
	year, mon, dom := utc.Date()
	dow := int(utc.Weekday())

	switch b {
	case board.EX4:
		// +1, not the plain dow above: EX4 uses 1=Sunday..7=Saturday,
		// not EX2's 0=Sunday..6=Saturday - see rtc.EX4Writer.Set's own
		// doc comment for how that was confirmed. This preview frame
		// must match EX4Writer.Set(now)'s own internal computation
		// exactly (same -D accuracy promise as this function's top
		// comment) - found by TestRun_RTCWrite_RealRunSendsExactlyTheFrameDebugPrinted
		// failing after EX4Writer.Set's own dow fix, since this line
		// has its own separate copy of the computation, not a shared
		// helper.
		ex4Dow := dow + 1
		frame, ferr := mcu.EX4DateTimeFrame(year, int(mon), dom, ex4Dow, utc.Hour(), utc.Minute(), utc.Second())
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
				w := rtc.EX4Writer{MCU: mcu.NewEX4(p)}
				if err := w.Set(now); err != nil {
					return err
				}
				fmt.Printf("rtc: system time: %s\n", utc.Format(rtcTimeLayout))
				return nil
			},
		}, exitOK
	case board.EX2:
		frame, ferr := mcu.EX2DateTimeFrame(year, int(mon), dom, dow, utc.Hour(), utc.Minute(), utc.Second())
		return &action{
			describe:   fmt.Sprintf("write current time (%s) to EX2 MCU RTC (%s)", now.Format(time.RFC3339), serialPath),
			debugLines: frameDebugLines(ferr, frame, serialPath, ex2LegacyBaud),
			planErr:    ferr,
			apply: func() error {
				p, err := mcu.Open(serialPath, ex2LegacyBaud)
				if err != nil {
					return err
				}
				defer p.Close()
				w := rtc.EX2Writer{MCU: mcu.NewEX2(p)}
				if err := w.Set(now); err != nil {
					return err
				}
				fmt.Printf("rtc: system time: %s\n", utc.Format(rtcTimeLayout))
				return nil
			},
		}, exitOK
	default:
		return unsupportedErr("rtc is not supported on board %v", b)
	}
}

// buildRTCReadAction handles both "r" (print only) and "s" (also set
// the host's system clock): sync is identical to read except for
// what happens with the decoded time, so sharing one builder avoids
// duplicating the whole request/reply/decode plumbing for a
// one-line difference. Both EX2 and EX4 are supported - each board's
// request frame/baud/reply decoder differ (see mcu.EX2DecodeDateAndTime/
// EX4DecodeDateAndTime's own doc comments for how each was
// confirmed), captured below via a few small per-board closures
// rather than duplicating the whole retry/print/sync body twice.
func buildRTCReadAction(b board.Board, serialPath string, debug, sync bool) (*action, int) {
	var (
		baud         int
		boardName    string
		requestFrame []byte
		getReply     func(p *mcu.Port) ([]byte, error)
		decode       func(reply []byte) (year, mon, dom, hour, min, sec int, err error)
	)
	switch b {
	case board.EX2:
		baud, boardName = ex2LegacyBaud, "EX2"
		requestFrame = mcu.EX2DateTimeRequestFrame()
		getReply = func(p *mcu.Port) ([]byte, error) { return mcu.NewEX2(p).DateAndTimeReply(ex2RTCReplyTimeout) }
		decode = func(reply []byte) (year, mon, dom, hour, min, sec int, err error) {
			year, mon, dom, _, hour, min, sec, err = mcu.EX2DecodeDateAndTime(reply)
			return
		}
	case board.EX4:
		baud, boardName = 19200, "EX4"
		requestFrame = mcu.EX4DateTimeRequestFrame()
		getReply = func(p *mcu.Port) ([]byte, error) { return mcu.NewEX4(p).DateAndTimeReply(ex2RTCReplyTimeout) }
		decode = func(reply []byte) (year, mon, dom, hour, min, sec int, err error) {
			year, mon, dom, _, hour, min, sec, err = mcu.EX4DecodeDateAndTime(reply)
			return
		}
	default:
		verb := "read"
		if sync {
			verb = "sync"
		}
		return &action{
			describe: fmt.Sprintf("%s the MCU RTC", verb),
			planErr:  fmt.Errorf("rtc %s is not supported on board %v", verb, b),
		}, exitOK
	}

	return &action{
		describe:   fmt.Sprintf("request the current time from %s MCU RTC (%s)", boardName, serialPath),
		debugLines: frameDebugLines(nil, requestFrame, serialPath, baud),
		apply: func() error {
			p, err := mcu.Open(serialPath, baud)
			if err != nil {
				return err
			}
			defer p.Close()

			// Retries a decode failure (not a "no reply at all", which
			// returns immediately below - see ex2RTCMaxAttempts) up to
			// ex2RTCMaxAttempts times; each attempt re-flushes and
			// re-sends via DateAndTimeReply, not just re-reading
			// whatever's already buffered. Reused as-is for EX4 - this
			// is defense against a genuine decode failure (garbled/no
			// reply), not specific to EX2's own now-understood-and-
			// fixed write-then-read corruption (see BUILDING.md), so
			// there's no reason it wouldn't also help EX4.
			var reply []byte
			var year, mon, dom, hour, min, sec int
			var derr error
			for attempt := 1; ; attempt++ {
				reply, err = getReply(p)
				if err != nil {
					return err
				}
				if debug {
					fmt.Printf("debug: reply (attempt %d)=% X\n", attempt, reply)
				}
				if len(reply) == 0 {
					fmt.Printf("rtc: no reply from MCU within %v\n", ex2RTCReplyTimeout)
					return nil
				}
				year, mon, dom, hour, min, sec, derr = decode(reply)
				if derr == nil || attempt >= ex2RTCMaxAttempts {
					break
				}
			}
			if derr != nil {
				// derr's own text embeds the raw reply bytes - not
				// printed here so -D stays the only way to see them.
				fmt.Printf("rtc: couldn't decode a time from the MCU's reply after %d attempts (run with -D to see the raw bytes)\n", ex2RTCMaxAttempts)
				return nil
			}
			// The RTC stores UTC, not local time (see pkg/rtc's own
			// package doc comment for how that was confirmed) - tagged
			// time.UTC here, matching the vendor rtc -r's own
			// unconverted "RTC time = ..." wording below, and matching
			// the real absolute moment SetSystemTime(t) needs for
			// "s" to actually set the right system time rather than
			// one off by the host's own timezone offset. The decoded
			// dow (discarded above, both boards) is never used here:
			// time.Date computes its own Weekday() from year/mon/dom,
			// which is also why EX4's own 1-7 vs EX2's 0-6 dow
			// convention (see rtc.EX4Writer.Set) never has to be
			// reconciled on the read side at all.
			t := time.Date(year, time.Month(mon), dom, hour, min, sec, 0, time.UTC)
			fmt.Printf("rtc: RTC time = %s\n", t.Format(rtcTimeLayout))
			if !sync {
				return nil
			}
			if err := rtc.SetSystemTime(t); err != nil {
				return err
			}
			fmt.Printf("rtc: system time set to %s\n", t.Local().Format(rtcTimeLayout))
			return nil
		},
	}, exitOK
}

// ex2uTempReplyTimeout is how long buildTempAction waits for a reply
// after sending the tmp_get request before reporting "no reply" -
// generous relative to the EX2U UART's own byte timing (a handful of
// milliseconds per frame, per SPEC.md) and relative to a real
// capture, where the reply arrived well under 1s.
const ex2uTempReplyTimeout = 2 * time.Second

// buildTempAction sends EX2U's tmp_get request and prints the
// decoded Celsius value (see mcu.EX2UDecodeTempCelsius) - matching
// the vendor tmp_get_c's own "Temperature: N C" wording. The raw
// reply bytes are NOT printed by default (-D/--debug already exists
// for exactly that - no need for a second, always-on way to see
// them); if decoding ever fails on a real device, the error message
// points at -D rather than dumping the bytes unconditionally.
// EX2U-only: no temp command is documented for EX2/EX4's legacy
// protocol (see SPEC.md's command tables).
func buildTempAction(b board.Board, serialPath string, args []string, debug bool) (*action, int) {
	if b != board.EX2U {
		return unsupportedErr("temp is not supported on board %v", b)
	}
	frame := mcu.EX2UTempRequestFrame()
	return &action{
		describe:   fmt.Sprintf("request temperature from EX2U MCU (%s)", serialPath),
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
			if debug {
				fmt.Printf("debug: reply=% X\n", reply)
			}
			if len(reply) == 0 {
				fmt.Printf("temp: no reply from MCU within %v\n", ex2uTempReplyTimeout)
				return nil
			}
			c, derr := mcu.EX2UDecodeTempCelsius(reply)
			if derr != nil {
				// derr's own text embeds the raw reply bytes (see
				// EX2UDecodeTempFahrenheit) - not printed here so
				// -D stays the only way to see them, same as the
				// success path above.
				fmt.Println("temp: couldn't decode a temperature from the MCU's reply (run with -D to see the raw bytes)")
				return nil
			}
			fmt.Printf("Temperature: %d C\n", c)
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
