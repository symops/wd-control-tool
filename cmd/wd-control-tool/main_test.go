package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/pkg/board"
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

func TestRun_RequiresBoardAndAction(t *testing.T) {
	if code, _, errOut := captureRun(t, []string{"-f", "50"}); code != exitUsage || !strings.Contains(errOut, "-b/--board") {
		t.Errorf("no -b: code=%d err=%q", code, errOut)
	}
	if code, _, errOut := captureRun(t, []string{"-b", "ex4"}); code != exitUsage || !strings.Contains(errOut, "-l/--led") {
		t.Errorf("no action: code=%d err=%q", code, errOut)
	}
	if code, _, errOut := captureRun(t, []string{"-b", "nosuchboard", "-f", "50"}); code != exitUsage || !strings.Contains(errOut, "unknown board") {
		t.Errorf("bad board: code=%d err=%q", code, errOut)
	}
}

// -b/--board is looked up in a dedicated first pass specifically so
// its position relative to the mode flags never matters (led's arity
// depends on the board, which would otherwise make order significant
// - see TestParseArgs_BoardAnywhereBeforeModeFlags). Confirm that
// holds end-to-end through run(), not just at the parseArgs level.
func TestRun_BoardPositionDoesNotMatter(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-f", "3", "-b", "ex2", "-n"})
	if code != exitOK {
		t.Errorf("code=%d out=%q, want success even with -b after -f", code, out)
	}
	if !strings.Contains(out, "level 3") {
		t.Errorf("out=%q, want the fan action to have been parsed correctly", out)
	}
}

// Omitting -b/--board entirely, with a mode flag present, must still
// be a usage error (there is nothing to resolve led's arity against,
// and no board to act on regardless).
func TestRun_MissingBoardEntirely(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-f", "3"})
	if code != exitUsage || !strings.Contains(errOut, "-b/--board is required") {
		t.Errorf("code=%d err=%q", code, errOut)
	}
}

func TestRun_MultipleActionsRunInOrder(t *testing.T) {
	// The exact example from the user's own request.
	code, out, _ := captureRun(t, []string{"-b", "ex2", "-n", "-D", "--led", "power", "blue", "on", "-f", "1"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	ledIdx := strings.Index(out, "power")
	fanIdx := strings.Index(out, "level 1")
	if ledIdx == -1 || fanIdx == -1 || ledIdx > fanIdx {
		t.Errorf("out=%q, want the led action described before the fan action", out)
	}
}

func TestRun_MultipleActions_FailFastOnRealRun(t *testing.T) {
	// Without -n: an invalid first action must stop the whole command
	// before a second (otherwise-valid) action ever runs. Point both
	// actions' serial traffic (fan on ex2u) and (display on ex4 -
	// not reached) is awkward to arrange on one board, so instead use
	// two fan actions on ex2u where the first is invalid and the
	// second is valid; if fail-fast works, the valid one's frame
	// bytes never appear anywhere because it never got dispatched (we
	// can't observe "didn't run" directly without touching hardware,
	// so this test instead checks the exit code and error message
	// come from the FIRST action, not generic/from the second).
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-f", "42", "-f", "50"})
	if code != exitUsage {
		t.Errorf("code=%d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "unsupported fan level 42") {
		t.Errorf("errOut=%q, want it to mention the FIRST (invalid) action's level 42, not the second", errOut)
	}
}

// Same fail-fast idea, but the SECOND action is the one that's wrong
// (a hard "unsupported on this board" error, not a planErr "would
// fail") - and this time under -n, since dry-run is unaffected by
// hard usage/unsupported errors the same way it neutralizes planErr.
// The first action must still have been described before the second
// one's hard error stops everything with the second action's own
// exit code (2), not the first action's (which would be 0).
func TestRun_MultipleActions_SecondActionUnsupportedStopsChain(t *testing.T) {
	code, out, errOut := captureRun(t, []string{"-b", "ex2u", "-n", "-f", "50", "-r", "write"})
	if code != exitUnsupported {
		t.Errorf("code=%d, want %d; out=%q err=%q", code, exitUnsupported, out, errOut)
	}
	if !strings.Contains(out, "would send fan 50%") {
		t.Errorf("out=%q, want the first action to have been described before the second one's hard error", out)
	}
	if !strings.Contains(errOut, "rtc is not supported") {
		t.Errorf("errOut=%q", errOut)
	}
}

func TestRun_RepeatedModeFlagAllowed(t *testing.T) {
	// Chaining the same mode twice is allowed (e.g. two fan levels in
	// sequence) - nothing in the new grammar needs to forbid it.
	code, out, _ := captureRun(t, []string{"-b", "ex2u", "-n", "-f", "25", "-f", "75"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "25%") || !strings.Contains(out, "75%") {
		t.Errorf("out=%q, want both fan levels described", out)
	}
}

func TestRun_DryRun_EX2UFan(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2u", "-n", "-D", "-f", "50"})
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
	code, out, _ := captureRun(t, []string{"-b", "ex2u", "-n", "-f", "42"})
	if code != exitOK {
		t.Errorf("code=%d, want 0 (dry-run must exit 0 even when the real run would fail)", code)
	}
	if !strings.Contains(out, "would fail") {
		t.Errorf("out=%q, want a 'would fail' line", out)
	}
}

func TestRun_RealRun_EX2UFan_InvalidLevelExitsUsage(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-f", "42"})
	if code != exitUsage {
		t.Errorf("code=%d, want %d (invalid level without -n)", code, exitUsage)
	}
	if !strings.Contains(errOut, "unsupported fan level") {
		t.Errorf("err=%q", errOut)
	}
}

func TestRun_DryRun_EX4Led(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex4", "-n", "-D", "--led", "power", "blue", "on"})
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
	code, out, _ := captureRun(t, []string{"-b", "ex4", "-n", "-D", "-d", "Booting..."})
	if code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out, "FA 04 01 00 0A 42 6F 6F 74 69 6E 67 2E 2E 2E FB") {
		t.Errorf("out=%q, want the confirmed SysName frame", out)
	}
}

// A display text that itself looks like a flag must still be taken
// verbatim as the TEXT argument - this is the whole point of fixed
// arity over a "-c ... take the rest" delimiter.
func TestRun_DisplayTextLookingLikeAFlag(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex4", "-n", "-D", "-d", "-not-a-flag"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !strings.Contains(out, `"-not-a-flag"`) {
		t.Errorf("out=%q, want the literal text in the description", out)
	}
}

// Regression test for the real bug fixed alongside the toBCD one:
// mcu.EX4DisplayFrame's LEN byte silently wrapped for text 256 bytes
// or longer. Confirm the CLI surfaces this as a normal "would fail"
// under -n (not a panic or a wrong-but-silent frame), and as a normal
// usage-class failure on a real run.
func TestRun_DisplayTextTooLong(t *testing.T) {
	text256 := strings.Repeat("A", 256)
	code, out, _ := captureRun(t, []string{"-b", "ex4", "-n", "-d", text256})
	if code != exitOK || !strings.Contains(out, "would fail") {
		t.Errorf("code=%d out=%q", code, out)
	}

	text255 := strings.Repeat("A", 255)
	code, out, _ = captureRun(t, []string{"-b", "ex4", "-n", "-d", text255})
	if code != exitOK || strings.Contains(out, "would fail") {
		t.Errorf("255 bytes should still be accepted: code=%d out=%q", code, out)
	}
}

func TestRun_DisplayUnsupportedOnEX2AndEX2U(t *testing.T) {
	for _, b := range []string{"ex2", "ex2u"} {
		code, _, errOut := captureRun(t, []string{"-b", b, "-n", "-d", "hi"})
		if code != exitUnsupported {
			t.Errorf("board %s: code=%d, want %d", b, code, exitUnsupported)
		}
		if !strings.Contains(errOut, "no display") {
			t.Errorf("board %s: err=%q", b, errOut)
		}
	}
}

func TestRun_RTCWriteUnimplementedOnEX2(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2", "-n", "-r", "write"})
	if code != exitOK {
		t.Errorf("code=%d, want 0 (dry-run always exits 0 for a 'would fail' case)", code)
	}
	if !strings.Contains(out, "would fail") {
		t.Errorf("out=%q, want a 'would fail' line mentioning EX2's RTC gap", out)
	}
}

func TestRun_RTCUnsupportedOnEX2U(t *testing.T) {
	code, _, _ := captureRun(t, []string{"-b", "ex2u", "-n", "-r", "write"})
	if code != exitUnsupported {
		t.Errorf("code=%d, want %d", code, exitUnsupported)
	}
}

func TestRun_RealRun_HardwareIOErrorOnBadSerialPath(t *testing.T) {
	// A path that cannot exist makes mcu.Open fail at os.OpenFile,
	// before any real device is touched - a safe way to exercise the
	// exitHardwareIO path without hardware.
	bad := "/nonexistent/wd-control-tool-test-path"
	cases := [][]string{
		{"-b", "ex2u", "--serial", bad, "-f", "50"},
		{"-b", "ex4", "--serial", bad, "-d", "hi"},
		{"-b", "ex4", "--serial", bad, "-r", "write"},
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
	code, out, _ := captureRun(t, []string{"-b", "ex2", "-n", "-l", "hd2", "red", "on"})
	if code != exitOK || !strings.Contains(out, "would fail") {
		t.Errorf("ex2 hd2: code=%d out=%q", code, out)
	}
	// Same zone is fine on EX4.
	code, out, _ = captureRun(t, []string{"-b", "ex4", "-n", "-l", "hd2", "red", "on"})
	if code != exitOK || !strings.Contains(out, "would") || strings.Contains(out, "would fail") {
		t.Errorf("ex4 hd2: code=%d out=%q", code, out)
	}
}

// Every other led test in this file only ever used "power"/"blue"/
// "on" (plus "hd2" above) - found via `go test -cover` that hd0/hd1
// and the off/blink states were never exercised through the CLI at
// all. Every zone/color/state combination EX4 supports must parse
// without error under dry-run.
func TestRun_LEDAllZonesColorsStates(t *testing.T) {
	for _, zone := range []string{"power", "hd0", "hd1", "hd2", "hd3"} {
		for _, color := range []string{"red", "blue"} {
			for _, state := range []string{"off", "on", "blinking", "blink"} {
				code, out, errOut := captureRun(t, []string{"-b", "ex4", "-n", "-l", zone, color, state})
				if code != exitOK || !strings.Contains(out, "would") || strings.Contains(out, "would fail") {
					t.Errorf("%s/%s/%s: code=%d out=%q err=%q", zone, color, state, code, out, errOut)
				}
			}
		}
	}
}

func TestParseArgs_AttachedLongForms(t *testing.T) {
	opts, err := parseArgs([]string{"--board=ex4", "--serial=/dev/ttyS2", "-f", "2"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if opts.board != "ex4" || opts.serial != "/dev/ttyS2" {
		t.Errorf("opts = %+v", opts)
	}
	if len(opts.actions) != 1 || opts.actions[0].mode != "fan" || len(opts.actions[0].args) != 1 || opts.actions[0].args[0] != "2" {
		t.Errorf("actions = %+v", opts.actions)
	}
}

func TestParseArgs_BoardAnywhereBeforeModeFlags(t *testing.T) {
	a, err := parseArgs([]string{"-D", "-b", "ex4", "-n", "-f", "2"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	b, err := parseArgs([]string{"-b", "ex4", "-n", "-D", "-f", "2"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.board != b.board || a.dryRun != b.dryRun || a.debug != b.debug || len(a.actions) != len(b.actions) {
		t.Errorf("flag order before the mode flag should not matter: a=%+v b=%+v", a, b)
	}
}

func TestParseArgs_ModifiersAfterAction(t *testing.T) {
	// Unlike the old -c-delimited grammar, -n/-D are ordinary flags
	// now and may appear AFTER an action's fixed-arity arguments too.
	opts, err := parseArgs([]string{"-b", "ex4", "-f", "2", "-n", "-D"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !opts.dryRun || !opts.debug {
		t.Errorf("opts = %+v, want dryRun and debug both true", opts)
	}
}

func TestParseArgs_UnrecognizedFlag(t *testing.T) {
	if _, err := parseArgs([]string{"-b", "ex4", "-f", "-x", "2"}); err == nil {
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

func TestParseArgs_LedArityDependsOnBoard(t *testing.T) {
	a, err := parseArgs([]string{"-b", "ex2u", "-l", "breathing"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(a.actions) != 1 || len(a.actions[0].args) != 1 {
		t.Errorf("ex2u led actions = %+v, want 1 action with 1 arg", a.actions)
	}

	b, err := parseArgs([]string{"-b", "ex4", "-l", "power", "blue", "on"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(b.actions) != 1 || len(b.actions[0].args) != 3 {
		t.Errorf("ex4 led actions = %+v, want 1 action with 3 args", b.actions)
	}
}

func TestRun_EmptyArgs(t *testing.T) {
	code, _, errOut := captureRun(t, []string{})
	if code != exitUsage || !strings.Contains(errOut, "-b/--board") {
		t.Errorf("no args: code=%d err=%q", code, errOut)
	}
}

func TestRun_ActionMissingTrailingArgs(t *testing.T) {
	cases := [][]string{
		{"-b", "ex4", "-f"},
		{"-b", "ex4", "-r"},
		{"-b", "ex4", "-d"},
		{"-b", "ex4", "-l"},
		{"-b", "ex2u", "-l"},
		{"-b", "ex4", "-l", "power", "blue"}, // 2 of the 3 needed
	}
	for _, args := range cases {
		code, _, errOut := captureRun(t, args)
		if code != exitUsage {
			t.Errorf("%v: code=%d, want %d; err=%q", args, code, exitUsage, errOut)
		}
	}
}

func TestRun_RTCWrongSubcommand(t *testing.T) {
	// Only "write" is a recognized rtc subcommand right now (no read
	// support - see SPEC.md); anything else is a usage error on every
	// board, dry-run or not, since it's a malformed command, not a
	// "would fail" semantic case.
	for _, bad := range []string{"read", "sync", "WRITE", ""} {
		code, _, errOut := captureRun(t, []string{"-b", "ex4", "-n", "-r", bad})
		if code != exitUsage {
			t.Errorf("rtc %q: code=%d err=%q, want %d", bad, code, errOut, exitUsage)
		}
	}
}

func TestRun_FanNonNumericLevel(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-n", "-f", "fifty"})
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
		code, _, errOut := captureRun(t, []string{"-b", b, "-n", "-f", "0"})
		if code != exitOK {
			t.Errorf("board %s fan 0: code=%d err=%q", b, code, errOut)
		}
	}
}

func TestRun_DebugAloneWithoutDryRunStillExecutesDescribeOnly(t *testing.T) {
	// -D without -n on a "would fail" case must still report the
	// failure (not silently swallow it because debug lines were
	// printed first).
	code, out, errOut := captureRun(t, []string{"-b", "ex2u", "-D", "-f", "42"})
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

// Under -D, a "would fail" action must only print its one describe
// line, never the frame-bytes/register-address detail lines (there
// is nothing real to show - frameDebugLines/regWriteDebugLines both
// return nil when planErr != nil). A regression here would leak a
// stale or zero-valued frame/address that was never actually going
// to be sent.
func TestRun_DebugLinesSuppressedOnWouldFail(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2u", "-n", "-D", "-f", "42"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	debugLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "debug:") {
			debugLines++
		}
	}
	if debugLines != 1 {
		t.Errorf("out=%q, want exactly 1 debug: line (the describe line only), got %d", out, debugLines)
	}
}

// Under -n, every action in a multi-action command is still
// described, even after an earlier one reports "would fail" - dry-run
// never stops early, unlike a real run (see
// TestRun_MultipleActions_FailFastOnRealRun).
func TestRun_DryRun_MultipleActionsAllDescribedEvenAfterAFailure(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2u", "-n", "-f", "42", "-f", "50"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "would fail") {
		t.Errorf("out=%q, want the first action's failure reported", out)
	}
	if !strings.Contains(out, "would send fan 50%") {
		t.Errorf("out=%q, want the second action still described despite the first failing", out)
	}
}

// buildLEDAction checks zone, then color, then state - with every
// field invalid, the zone error must be the one reported (pin the
// priority so a refactor can't silently reorder it and confuse users
// about which field is actually wrong).
func TestRun_LEDMultipleInvalidFields_ZoneErrorWins(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex4", "-n", "-l", "badzone", "badcolor", "badstate"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !strings.Contains(out, `unknown led zone "badzone"`) {
		t.Errorf("out=%q, want the zone error specifically", out)
	}
}

// Completes the priority chain: with a VALID zone but both color and
// state invalid, the color error must win (buildLEDAction checks
// zone, then color, then state, each only overwriting err if it was
// still nil).
func TestRun_LEDMultipleInvalidFields_ColorErrorWinsOverState(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex4", "-n", "-l", "power", "badcolor", "badstate"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !strings.Contains(out, `unknown led color "badcolor"`) {
		t.Errorf("out=%q, want the color error specifically", out)
	}
}

func TestModeNameFor(t *testing.T) {
	cases := map[string]string{
		"-l": "led", "--led": "led",
		"-f": "fan", "--fan": "fan",
		"-r": "rtc", "--rtc": "rtc",
		"-d": "display", "--display": "display",
		"-x": "", "--unknown": "", "": "",
	}
	for flag, want := range cases {
		if got := modeNameFor(flag); got != want {
			t.Errorf("modeNameFor(%q) = %q, want %q", flag, got, want)
		}
	}
}

func TestArityFor(t *testing.T) {
	cases := []struct {
		mode string
		b    board.Board
		want int
	}{
		{"fan", board.EX2U, 1}, {"fan", board.EX2, 1}, {"fan", board.EX4, 1},
		{"display", board.EX4, 1},
		{"rtc", board.EX4, 1},
		{"led", board.EX2U, 1},
		{"led", board.EX2, 3},
		{"led", board.EX4, 3},
	}
	for _, c := range cases {
		if got := arityFor(c.mode, c.b); got != c.want {
			t.Errorf("arityFor(%q, %v) = %d, want %d", c.mode, c.b, got, c.want)
		}
	}
}

// Short and long forms of every flag must parse identically - a
// future edit to one spelling's case in parseArgs shouldn't be able
// to silently diverge from the other.
func TestParseArgs_ShortAndLongFormsAreEquivalent(t *testing.T) {
	pairs := [][2][]string{
		{{"-b", "ex4", "-f", "2"}, {"--board", "ex4", "--fan", "2"}},
		{{"-b", "ex4", "-d", "hi"}, {"--board", "ex4", "--display", "hi"}},
		{{"-b", "ex4", "-r", "write"}, {"--board", "ex4", "--rtc", "write"}},
		{{"-b", "ex4", "-l", "power", "blue", "on"}, {"--board", "ex4", "--led", "power", "blue", "on"}},
	}
	for _, pair := range pairs {
		a, errA := parseArgs(pair[0])
		b, errB := parseArgs(pair[1])
		if errA != nil || errB != nil {
			t.Fatalf("%v / %v: errA=%v errB=%v", pair[0], pair[1], errA, errB)
		}
		if a.board != b.board || len(a.actions) != len(b.actions) {
			t.Errorf("%v / %v: a=%+v b=%+v", pair[0], pair[1], a, b)
			continue
		}
		for i := range a.actions {
			if a.actions[i].mode != b.actions[i].mode || !equalStrings(a.actions[i].args, b.actions[i].args) {
				t.Errorf("%v / %v: action %d differs: a=%+v b=%+v", pair[0], pair[1], i, a.actions[i], b.actions[i])
			}
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Every other test exercises runAction either under -n, or on a path
// that fails before apply() is ever called (bad args, a nonexistent
// --serial path) - a REAL action whose apply() actually succeeds had
// no coverage at all (confirmed via `go test -cover`: the final
// `return exitOK` after a.apply() succeeds was never hit). Build an
// action directly (bypassing buildAction/real hardware) to close
// that gap.
func TestRunAction_SuccessfulRealRunIsSilentAndOK(t *testing.T) {
	applied := false
	a := &action{
		describe: "do the thing",
		apply:    func() error { applied = true; return nil },
	}

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, errW
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()

	code := runAction(a, false, false)

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = origOut, origErr
	out, errOut := <-outCh, <-errCh

	if code != exitOK {
		t.Errorf("code=%d, want %d", code, exitOK)
	}
	if !applied {
		t.Error("apply() was never called")
	}
	if out != "" || errOut != "" {
		t.Errorf("a successful real run must be silent: out=%q err=%q", out, errOut)
	}
}

func TestRunAction_RealRunApplyError(t *testing.T) {
	a := &action{
		describe: "do the thing",
		apply:    func() error { return errors.New("boom") },
	}
	code, _, errOut := captureRunAction(t, a, false, false)
	if code != exitHardwareIO {
		t.Errorf("code=%d, want %d", code, exitHardwareIO)
	}
	if !strings.Contains(errOut, "boom") {
		t.Errorf("errOut=%q", errOut)
	}
}

// captureRunAction mirrors captureRun but drives runAction directly,
// for tests that need a hand-built *action rather than a full argv.
func captureRunAction(t *testing.T, a *action, dryRun, debug bool) (code int, stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, errW
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()

	code = runAction(a, dryRun, debug)

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = origOut, origErr
	return code, <-outCh, <-errCh
}

// parseArgs does its own bounds-checked indexing/slicing (i++,
// args[start:end]) rather than going through the stdlib flag
// package, which is exactly the kind of hand-rolled parsing logic
// that tends to hide an off-by-one. It must never panic, only ever
// return a plain error, no matter how malformed the input is. Run
// with `go test -fuzz=FuzzParseArgs_NeverPanics ./cmd/wd-control-tool`
// to actually fuzz beyond the seed corpus.
func FuzzParseArgs_NeverPanics(f *testing.F) {
	for _, seed := range []string{
		"-b ex4 -f 3",
		"-b ex2u --led breathing",
		"",
		"-b",
		"--serial",
		"-l",
		"-f -f -f -f",
		"--board=",
		"--serial=",
		"-b ex4 -l",
		"-b ex4 -l power",
		"-b ex4 -l power blue",
		"-h -v -b ex4 -f 1",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		args := strings.Fields(s)
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseArgs(%q) panicked: %v", args, r)
			}
		}()
		_, _ = parseArgs(args)
	})
}

// Ties printUsage's own "Examples:" section to a test: every example
// a user would copy-paste must actually parse and dry-run
// successfully, so documentation drift (a syntax change that makes
// an old example stop working, the way removing -c did) is a test
// failure, not a silent inconsistency a user discovers the hard way.
// These are literal copies of the lines in printUsage - keep them in
// sync by hand if that text changes, there is no automatic
// extraction from the doc string.
func TestRun_UsageExamplesActuallyWork(t *testing.T) {
	examples := [][]string{
		{"-b", "ex2", "-f", "3"},
		{"-b", "ex4", "--display", "Booting..."},
		{"-b", "ex2", "--led", "power", "blue", "on", "-f", "1"},
	}
	for _, args := range examples {
		withDryRun := append(append([]string{}, args...), "-n")
		code, _, errOut := captureRun(t, withDryRun)
		if code != exitOK {
			t.Errorf("example %v: code=%d err=%q", args, code, errOut)
		}
	}
}

// Same idea, for README.md's own (longer, and partly overlapping)
// "Quick start" example list - a different document than printUsage,
// kept in sync by hand just the same, so tested separately in case
// one drifts without the other. Two examples (the last two) already
// include -n/-D themselves and are used as-is.
func TestRun_ReadmeExamplesActuallyWork(t *testing.T) {
	examples := [][]string{
		{"-b", "ex2u", "-f", "50"},
		{"-b", "ex2u", "-l", "breathing"},
		{"-b", "ex2", "-f", "3"},
		{"-b", "ex2", "--led", "power", "blue", "on"},
		{"-b", "ex4", "-f", "2"},
		{"-b", "ex4", "--led", "power", "blue", "on"},
		{"-b", "ex4", "--display", "Booting..."},
		{"-b", "ex4", "-r", "write"},
		{"-b", "ex2", "--led", "power", "blue", "on", "-f", "1"},
		{"-b", "ex4", "-n", "-D", "--led", "power", "blue", "on"},
	}
	for _, args := range examples {
		withDryRun := args
		hasDryRun := false
		for _, a := range args {
			if a == "-n" {
				hasDryRun = true
			}
		}
		if !hasDryRun {
			withDryRun = append(append([]string{}, args...), "-n")
		}
		code, _, errOut := captureRun(t, withDryRun)
		if code != exitOK {
			t.Errorf("README example %v: code=%d err=%q", args, code, errOut)
		}
	}
}

// Chaining several actions in one command is this session's headline
// feature (replacing -c) - confirm a long chain (200 actions) doesn't
// hit some unexpected slowdown or resource limit in parseArgs' own
// loop/slicing or in the sequential dispatch loop in run(). All
// dry-run, so this only exercises the parsing/dispatch path, not real
// hardware timing.
func TestRun_LongActionChainDoesNotMisbehave(t *testing.T) {
	const n = 200
	args := []string{"-b", "ex2u", "-n"}
	for i := 0; i < n; i++ {
		args = append(args, "-f", "50")
	}

	start := time.Now()
	code, out, errOut := captureRun(t, args)
	elapsed := time.Since(start)

	if code != exitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if got := strings.Count(out, "would send fan"); got != n {
		t.Errorf("got %d described actions, want %d", got, n)
	}
	if elapsed > 2*time.Second {
		t.Errorf("200 dry-run actions took %v, want well under 2s (no hardware I/O happens under -n)", elapsed)
	}
}
