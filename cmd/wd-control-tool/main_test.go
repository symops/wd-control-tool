package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureRun runs run(args) with os.Stdout/os.Stderr redirected to
// pipes, returning the exit code and both streams' text. Note: run()
// itself does real hardware I/O when a command isn't -n/--dry-run
// and its arguments are valid - every test below either uses -n, or
// deliberately supplies arguments that fail validation/argument-count
// checks before any device would be opened, or (for the "hardware IO
// error" cases) points --serial at a path that cannot possibly exist,
// so mcu.Open fails cleanly before touching anything. No test here
// calls regio.Open() on a real /dev/mem - that's intentionally left
// untested by automation; see BUILDING.md.
func captureRun(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()

	code = run(args)

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = origOut, origErr

	stdout = <-outCh
	stderr = <-errCh
	return
}

func TestRun_HelpAndVersionMustBeAlone(t *testing.T) {
	if code, out, _ := captureRun(t, []string{"-h"}); code != exitOK || !strings.Contains(out, "wd-control-tool -b BOARD") {
		t.Errorf("-h: code=%d out=%q", code, out)
	}
	if code, out, _ := captureRun(t, []string{"--help"}); code != exitOK || !strings.Contains(out, "wd-control-tool -b BOARD") {
		t.Errorf("--help: code=%d out=%q", code, out)
	}
	if code, out, _ := captureRun(t, []string{"-v"}); code != exitOK || !strings.Contains(out, "1.0.0") || !strings.Contains(out, "symops@gmail.com") {
		t.Errorf("-v: code=%d out=%q", code, out)
	}
	if code, _, errOut := captureRun(t, []string{"-h", "-v"}); code != exitUsage || !strings.Contains(errOut, "alone") {
		t.Errorf("-h -v: code=%d err=%q, want usage error mentioning 'alone'", code, errOut)
	}
	if code, _, errOut := captureRun(t, []string{"-v", "-b", "ex4"}); code != exitUsage || !strings.Contains(errOut, "alone") {
		t.Errorf("-v -b ex4: code=%d err=%q, want usage error mentioning 'alone'", code, errOut)
	}
}

func TestRun_RequiresBoardAndMode(t *testing.T) {
	if code, _, errOut := captureRun(t, []string{"-f", "-c", "50"}); code != exitUsage || !strings.Contains(errOut, "-b/--board") {
		t.Errorf("no -b: code=%d err=%q", code, errOut)
	}
	if code, _, errOut := captureRun(t, []string{"-b", "ex4", "-c", "50"}); code != exitUsage || !strings.Contains(errOut, "-l/--led") {
		t.Errorf("no mode: code=%d err=%q", code, errOut)
	}
	if code, _, errOut := captureRun(t, []string{"-b", "ex4", "-f"}); code != exitUsage || !strings.Contains(errOut, "-c/--command") {
		t.Errorf("no -c: code=%d err=%q", code, errOut)
	}
	if code, _, errOut := captureRun(t, []string{"-b", "nosuchboard", "-f", "-c", "50"}); code != exitUsage || !strings.Contains(errOut, "unknown board") {
		t.Errorf("bad board: code=%d err=%q", code, errOut)
	}
	if code, _, errOut := captureRun(t, []string{"-b", "ex4", "-f", "-l", "-c", "50"}); code != exitUsage || !strings.Contains(errOut, "only one of") {
		t.Errorf("two modes: code=%d err=%q", code, errOut)
	}
}

func TestRun_CommandMustBeLast(t *testing.T) {
	// -n/-D placed AFTER -c are swallowed as the command's own
	// arguments, not parsed as flags - this is documented behavior
	// (see printUsage), not a bug; pin it so a future refactor can't
	// silently change the ordering rule without a test noticing.
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-f", "-c", "50", "-n"})
	if code != exitUsage || !strings.Contains(errOut, "usage: -b BOARD -f -c LEVEL") {
		t.Errorf("-n after -c: code=%d err=%q, want a wrong-arg-count usage error", code, errOut)
	}
}

func TestRun_DryRun_EX2UFan(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2u", "-f", "-n", "-D", "-c", "50"})
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "FA 02 00 80 00 00 FB") {
		t.Errorf("out=%q, want the confirmed fan_set_50 frame", out)
	}
	if !strings.Contains(out, "would send fan 50% to EX2U MCU") {
		t.Errorf("out=%q, missing the dry-run description line", out)
	}
}

func TestRun_DryRun_EX2UFan_InvalidLevelExitsZero(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2u", "-f", "-n", "-c", "42"})
	if code != exitOK {
		t.Errorf("code=%d, want 0 (dry-run must exit 0 even when the real run would fail)", code)
	}
	if !strings.Contains(out, "would fail") {
		t.Errorf("out=%q, want a 'would fail' line", out)
	}
}

func TestRun_RealRun_EX2UFan_InvalidLevelExitsUsage(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-f", "-c", "42"})
	if code != exitUsage {
		t.Errorf("code=%d, want %d (invalid level without -n)", code, exitUsage)
	}
	if !strings.Contains(errOut, "unsupported fan level") {
		t.Errorf("err=%q", errOut)
	}
}

func TestRun_DryRun_EX4Led(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex4", "--led", "-n", "-D", "-c", "power", "blue", "on"})
	if code != exitOK {
		t.Fatalf("code=%d, out=%q", code, out)
	}
	for _, want := range []string{
		"0xf1010100: set=0x00000000 clear=0x10000000", // clear red in Data-Out
		"0xf1010100: set=0x20000000 clear=0x00000000", // set blue in Data-Out
	} {
		if !strings.Contains(out, want) {
			t.Errorf("out=%q, missing %q", out, want)
		}
	}
}

func TestRun_DryRun_EX4Display(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex4", "-d", "-n", "-D", "-c", "Booting..."})
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "FA 04 01 00 0A 42 6F 6F 74 69 6E 67 2E 2E 2E FB") {
		t.Errorf("out=%q, want the confirmed SysName frame", out)
	}
}

func TestRun_DisplayUnsupportedOnEX2AndEX2U(t *testing.T) {
	for _, b := range []string{"ex2", "ex2u"} {
		code, _, errOut := captureRun(t, []string{"-b", b, "-d", "-n", "-c", "hi"})
		if code != exitUnsupported {
			t.Errorf("board %s: code=%d, want %d", b, code, exitUnsupported)
		}
		if !strings.Contains(errOut, "no display") {
			t.Errorf("board %s: err=%q", b, errOut)
		}
	}
}

func TestRun_RTCWriteUnimplementedOnEX2(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2", "-r", "-n", "-c", "write"})
	if code != exitOK {
		t.Errorf("code=%d, want 0 (dry-run always exits 0 for a 'would fail' case)", code)
	}
	if !strings.Contains(out, "would fail") {
		t.Errorf("out=%q, want a 'would fail' line mentioning EX2's RTC gap", out)
	}
}

func TestRun_RTCUnsupportedOnEX2U(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-r", "-n", "-c", "write"})
	if code != exitUnsupported {
		t.Errorf("code=%d, want %d", code, exitUnsupported)
	}
	_ = errOut
}

func TestRun_RealRun_HardwareIOErrorOnBadSerialPath(t *testing.T) {
	// A path that cannot exist makes mcu.Open fail at os.OpenFile,
	// before any real device is touched - a safe way to exercise the
	// exitHardwareIO path without hardware.
	bad := "/nonexistent/wd-control-tool-test-path"
	cases := [][]string{
		{"-b", "ex2u", "-f", "--serial", bad, "-c", "50"},
		{"-b", "ex4", "-d", "--serial", bad, "-c", "hi"},
		{"-b", "ex4", "-r", "--serial", bad, "-c", "write"},
	}
	for _, args := range cases {
		code, _, errOut := captureRun(t, args)
		if code != exitHardwareIO {
			t.Errorf("%v: code=%d, want %d; err=%q", args, code, exitHardwareIO, errOut)
		}
	}
}

func TestRun_LEDZoneValidationPerBoard(t *testing.T) {
	// hd2/hd3 don't exist on EX2 - this must be caught before any
	// hardware is touched (dry-run exits 0 reporting "would fail").
	code, out, _ := captureRun(t, []string{"-b", "ex2", "-l", "-n", "-c", "hd2", "red", "on"})
	if code != exitOK || !strings.Contains(out, "would fail") {
		t.Errorf("ex2 hd2: code=%d out=%q", code, out)
	}
	// Same zone is fine on EX4.
	code, out, _ = captureRun(t, []string{"-b", "ex4", "-l", "-n", "-c", "hd2", "red", "on"})
	if code != exitOK || !strings.Contains(out, "would") || strings.Contains(out, "would fail") {
		t.Errorf("ex4 hd2: code=%d out=%q", code, out)
	}
}

func TestParseArgs_AttachedLongForms(t *testing.T) {
	opts, err := parseArgs([]string{"--board=ex4", "--serial=/dev/ttyS2", "-f", "-c", "2"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if opts.board != "ex4" || opts.serial != "/dev/ttyS2" || opts.mode != "fan" || !opts.commandGiven {
		t.Errorf("opts = %+v", opts)
	}
	if len(opts.cmdArgs) != 1 || opts.cmdArgs[0] != "2" {
		t.Errorf("cmdArgs = %v", opts.cmdArgs)
	}
}

func TestParseArgs_FlagOrderBeforeCIsFree(t *testing.T) {
	a, err := parseArgs([]string{"-D", "-f", "-b", "ex4", "-n", "-c", "2"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	b, err := parseArgs([]string{"-b", "ex4", "-n", "-D", "-f", "-c", "2"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.board != b.board || a.mode != b.mode || a.dryRun != b.dryRun || a.debug != b.debug {
		t.Errorf("flag order before -c should not matter: a=%+v b=%+v", a, b)
	}
}

func TestParseArgs_UnrecognizedFlag(t *testing.T) {
	if _, err := parseArgs([]string{"-b", "ex4", "-f", "-x", "-c", "2"}); err == nil {
		t.Error("want error for unrecognized flag -x")
	}
}

func TestParseArgs_MissingValueForBoardOrSerial(t *testing.T) {
	if _, err := parseArgs([]string{"-b"}); err == nil {
		t.Error("want error for -b with no value")
	}
	if _, err := parseArgs([]string{"--serial"}); err == nil {
		t.Error("want error for --serial with no value")
	}
}

func TestRun_EmptyArgs(t *testing.T) {
	code, _, errOut := captureRun(t, []string{})
	if code != exitUsage || !strings.Contains(errOut, "-b/--board") {
		t.Errorf("no args: code=%d err=%q", code, errOut)
	}
}

// -c with zero arguments following it is distinct from -c never
// appearing at all (commandGiven=true, cmdArgs=[]string{}) - every
// mode here needs at least one argument, so this must still be a
// wrong-arg-count usage error, not silently treated as "-c missing".
func TestRun_CommandWithZeroTrailingArgs(t *testing.T) {
	cases := [][]string{
		{"-b", "ex4", "-f", "-c"},
		{"-b", "ex4", "-r", "-c"},
		{"-b", "ex4", "-d", "-c"},
		{"-b", "ex4", "-l", "-c"},
		{"-b", "ex2u", "-l", "-c"},
	}
	for _, args := range cases {
		code, _, errOut := captureRun(t, args)
		if code != exitUsage {
			t.Errorf("%v: code=%d, want %d; err=%q", args, code, exitUsage, errOut)
		}
	}
}

func TestRun_LEDWrongArgCountPerBoard(t *testing.T) {
	// EX2U's led takes exactly 1 argument (a state), not 3.
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-l", "-c", "power", "blue", "on"})
	if code != exitUsage {
		t.Errorf("ex2u led with 3 args: code=%d err=%q", code, errOut)
	}
	// EX2/EX4's led takes exactly 3 arguments, not 1.
	code, _, errOut = captureRun(t, []string{"-b", "ex4", "-l", "-c", "breathing"})
	if code != exitUsage {
		t.Errorf("ex4 led with 1 arg: code=%d err=%q", code, errOut)
	}
}

func TestRun_RTCWrongSubcommand(t *testing.T) {
	// Only "write" is a recognized rtc subcommand right now (no read
	// support - see SPEC.md); anything else is a usage error on every
	// board, dry-run or not, since it's a malformed command, not a
	// "would fail" semantic case.
	for _, bad := range []string{"read", "sync", "WRITE", ""} {
		code, _, errOut := captureRun(t, []string{"-b", "ex4", "-r", "-n", "-c", bad})
		if code != exitUsage {
			t.Errorf("rtc %q: code=%d err=%q, want %d", bad, code, errOut, exitUsage)
		}
	}
}

func TestRun_FanNonNumericLevel(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-f", "-n", "-c", "fifty"})
	if code != exitUsage {
		t.Errorf("code=%d, want %d (non-numeric level is a usage error even under -n)", code, exitUsage)
	}
	if !strings.Contains(errOut, "integer") {
		t.Errorf("err=%q", errOut)
	}
}

func TestRun_FanUnsupportedBoard(t *testing.T) {
	// board.Parse only accepts ex2u/ex2/ex4, so there is no board
	// value that reaches buildFanAction's "default: unsupported"
	// branch today - this documents that every currently-parseable
	// board supports fan, and pins buildFanAction's behavior should a
	// fourth board ever be added without updating it.
	for _, b := range []string{"ex2u", "ex2", "ex4"} {
		code, _, errOut := captureRun(t, []string{"-b", b, "-f", "-n", "-c", "0"})
		if code != exitOK {
			t.Errorf("board %s fan 0: code=%d err=%q", b, code, errOut)
		}
	}
}

func TestRun_DebugAloneWithoutDryRunStillExecutesDescribeOnly(t *testing.T) {
	// -D without -n on a "would fail" case must still report the
	// failure (not silently swallow it because debug lines were
	// printed first).
	code, out, errOut := captureRun(t, []string{"-b", "ex2u", "-f", "-D", "-c", "42"})
	if code != exitUsage {
		t.Errorf("code=%d, want %d", code, exitUsage)
	}
	if !strings.Contains(out, "debug:") {
		t.Errorf("out=%q, want debug lines to still print", out)
	}
	if !strings.Contains(errOut, "unsupported fan level") {
		t.Errorf("errOut=%q", errOut)
	}
}
