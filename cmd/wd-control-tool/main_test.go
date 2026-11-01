package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/symops/wd-control-tool/pkg/board"
	"github.com/symops/wd-control-tool/pkg/rtc"
	"github.com/symops/wd-control-tool/test/fakemcu"
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

// -h/--help previously documented every flag EXCEPT -b/--board itself
// (the one actually-required flag) - fixed per explicit user request
// to cover every parameter, not just the action flags. Pin that the
// help text actually explains -b/--board's values and its
// repeated-value/conflict rule, not just mention it in the usage
// line.
func TestRun_HelpDocumentsBoardFlag(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-h"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	for _, want := range []string{"-b, --board BOARD", "ex2u", "ex2-ultra", "usage error", "harmless"} {
		if !strings.Contains(out, want) {
			t.Errorf("-h output missing %q: out=%q", want, out)
		}
	}
}

func TestRun_HelpAndVersionMustBeAlone(t *testing.T) {
	if code, out, _ := captureRun(t, []string{"-h"}); code != exitOK || !strings.Contains(out, "wd-control-tool -b BOARD") {
		t.Errorf("-h: code=%d out=%q", code, out)
	}
	if code, out, _ := captureRun(t, []string{"--help"}); code != exitOK || !strings.Contains(out, "wd-control-tool -b BOARD") {
		t.Errorf("--help: code=%d out=%q", code, out)
	}
	if code, out, _ := captureRun(t, []string{"-v"}); code != exitOK || !strings.Contains(out, "1.0") || !strings.Contains(out, "symops@gmail.com") {
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

// A second -b/--board with a DIFFERENT value must be a hard usage
// error, not silently let the last one win for every action in the
// command - confirmed confusing in practice: this exact command used
// to apply ex4 to the first action too, reporting a baffling
// "unsupported fan level 50" for a value that is perfectly valid on
// the ex2u the user actually wrote right before it.
// The pre-pass that scans for -b/--board's value (it may appear
// anywhere - see TestRun_BoardPositionDoesNotMatter) used to scan
// every raw token blindly, with no idea that some of them are
// actually ANOTHER action's own fixed-arity argument, not a second
// -b/--board flag. `-b ex4 -n -d -b` (display text that happens to
// literally BE "-b") used to fail with "-b requires a value" -
// confirmed by actually running it, not just reading the code - even
// though README/printUsage both promise display text is taken
// "verbatim, even if it looks like a flag". Each case below uses a
// different action whose own single argument is the string "-b",
// confirming each one is now correctly treated as that action's own
// verbatim argument (and fails, if at all, at THAT action's own
// validation layer - a bad fan level, an unknown rtc subcommand - not
// as a phantom second board flag).
// Sibling check for the modifier flags (-n/-D), not just -b/--board:
// an action's own consumed argument(s) must never be re-examined as
// a flag by the SECOND pass either - it jumps straight past them
// (i = end-1 after slicing args[start:end] for the action), so this
// should already hold by construction, but it's cheap to pin
// directly rather than rely on that alone.
func TestRun_DisplayTextLooksLikeModifierFlag(t *testing.T) {
	for _, text := range []string{"-n", "-D", "--dry-run", "--debug"} {
		code, out, errOut := captureRun(t, []string{"-b", "ex4", "-n", "-d", text})
		if code != exitOK {
			t.Errorf("text=%q: code=%d out=%q err=%q", text, code, out, errOut)
			continue
		}
		want := fmt.Sprintf("display text %q", text)
		if !strings.Contains(out, want) {
			t.Errorf("text=%q: out=%q, want it to contain %q", text, out, want)
		}
	}
}

func TestRun_ActionArgumentThatLooksLikeBoardFlag(t *testing.T) {
	cases := []struct {
		args     []string
		wantCode int
		wantText string // substring to find in stdout+stderr combined
	}{
		// Display text "-b" is perfectly valid, so this dry-runs clean.
		{[]string{"-b", "ex4", "-n", "-d", "-b"}, exitOK, `display text "-b"`},
		// Fan's level parse (strconv.Atoi) and rtc's subcommand check
		// both fail as hard usage errors, regardless of -n - not
		// through the planErr/"would fail" dry-run path.
		{[]string{"-b", "ex4", "-n", "-f", "-b"}, exitUsage, "fan level must be an integer"},
		{[]string{"-b", "ex4", "-n", "-r", "-b"}, exitUsage, "unknown rtc subcommand"},
		// led's zone validation DOES go through planErr, so this is a
		// dry-run "would fail", not a hard error - board (ex2) is
		// already known from -b appearing before -l here.
		{[]string{"-b", "ex2", "-n", "-l", "-b", "red", "on"}, exitOK, `would fail: unknown led zone "-b"`},
	}
	for _, c := range cases {
		code, out, errOut := captureRun(t, c.args)
		if code != c.wantCode {
			t.Errorf("%v: code=%d, want %d; out=%q err=%q", c.args, code, c.wantCode, out, errOut)
			continue
		}
		if !strings.Contains(out, c.wantText) && !strings.Contains(errOut, c.wantText) {
			t.Errorf("%v: out=%q err=%q, want one of them to contain %q", c.args, out, errOut, c.wantText)
		}
		if strings.Contains(errOut, "requires a value") {
			t.Errorf("%v: errOut=%q, the literal \"-b\" argument was misdetected as a second board flag", c.args, errOut)
		}
	}
}

func TestRun_ConflictingBoardValuesRejected(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-n", "-f", "50", "-b", "ex4", "-f", "2"})
	if code != exitUsage {
		t.Errorf("code=%d, want %d; err=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "conflicts") {
		t.Errorf("errOut=%q, want it to mention the conflict", errOut)
	}
}

// Repeating the SAME board value is harmless (if pointless) and must
// not be rejected.
func TestRun_RepeatedSameBoardValueAllowed(t *testing.T) {
	code, out, errOut := captureRun(t, []string{"-b", "ex4", "-n", "-f", "2", "-b", "ex4", "-f", "1"})
	if code != exitOK {
		t.Errorf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "level 2") || !strings.Contains(out, "level 1") {
		t.Errorf("out=%q, want both actions described", out)
	}
}

// ex2u and ex2-ultra are two spellings of the exact same board (see
// board.Parse) - setBoard must compare the *resolved* board.Board,
// not the raw strings, or this would be wrongly rejected as a
// conflict even though nothing about which board to use is actually
// ambiguous.
func TestRun_RepeatedSameBoardValueAllowed_ViaDifferentAlias(t *testing.T) {
	code, out, errOut := captureRun(t, []string{"-b", "ex2u", "-n", "-f", "50", "-b", "ex2-ultra", "-f", "25"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "fan 50%") || !strings.Contains(out, "fan 25%") {
		t.Errorf("out=%q, want both actions described", out)
	}
}

// An unparseable second -b/--board value must be reported as an
// unknown board, not swallowed into a (misleading) "conflicts with"
// message - the value itself is invalid regardless of what came
// before it.
func TestRun_SecondBoardValueInvalid_ReportsUnknownBoardNotConflict(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "-n", "-f", "50", "-b", "bogus"})
	if code != exitUsage {
		t.Errorf("code=%d, want %d; err=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "unknown board") {
		t.Errorf("errOut=%q, want it to report the unknown board value", errOut)
	}
	if strings.Contains(errOut, "conflicts") {
		t.Errorf("errOut=%q, should not frame an invalid value as a conflict", errOut)
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

// Same scenario as TestRun_MultipleActions_SecondActionUnsupportedStopsChain,
// but for a REAL (non-dry-run) run: confirms the first action's
// hardware write actually went out over the wire before the second
// action's hard "not supported" error stops the chain - the first
// action is NOT rolled back or held back pending validation of the
// rest of the chain. This is the real-world version of what README's
// "the first one that fails stops the rest" means: later actions
// never run, but an earlier one that already succeeded stays
// applied. Worth pinning explicitly since "unsupported on this
// board" is a purely static, pre-flight-checkable condition (unlike
// a hardware I/O error, which genuinely can only be discovered by
// trying) - a reader could otherwise assume the whole chain is
// validated before anything is touched.
func TestRun_MultipleActions_EarlierActionAlreadyAppliedBeforeLaterUnsupportedStopsChain(t *testing.T) {
	fake := fakemcu.New(t)

	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "--serial", fake.SlavePath, "-f", "50", "-r", "write"})
	if code != exitUnsupported {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(errOut, "rtc is not supported") {
		t.Errorf("errOut=%q", errOut)
	}

	got := fake.ReadFrame(t, 7, 2*time.Second)
	want := []byte{0xFA, 0x02, 0x00, 0x80, 0x00, 0x00, 0xFB} // EX2U fan 50% (0x80)
	if !bytes.Equal(got, want) {
		t.Errorf("wire bytes = % X, want % X (the fan action should have actually run before rtc's unsupported error stopped the chain)", got, want)
	}
}

// Contrasts with
// TestRun_MultipleActions_EarlierActionAlreadyAppliedBeforeLaterUnsupportedStopsChain:
// a syntax error (here, -l at the end of the command with no STATE
// argument after it) is caught entirely within parseArgs, BEFORE
// run() ever starts looping over actions - so unlike an "unsupported
// on this board" error (caught later, during dispatch, only after
// any earlier action has already run for real), a syntax error
// anywhere in a chain aborts the whole command atomically, including
// actions placed earlier that were themselves perfectly valid.
//
// Proven without needing a real or fake serial port at all: --serial
// points at a path that cannot exist, so if the fan action (listed
// first, and syntactically valid on its own) ever actually reached
// its apply() step, mcu.Open would fail and this would exit
// exitHardwareIO (3) instead - the same way
// TestRun_RealRun_HardwareIOErrorOnBadSerialPath proves that path
// fails cleanly when it IS reached. Getting exitUsage (1) here
// instead proves the fan action's apply() was never even attempted.
func TestRun_ChainWithTrailingSyntaxError_AbortsBeforeAnyActionRuns(t *testing.T) {
	bad := "/nonexistent/wd-control-tool-test-path"
	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "--serial", bad, "-f", "50", "-l"})
	if code != exitUsage {
		t.Fatalf("code=%d, want %d (a syntax error, caught before any action runs); err=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "requires") {
		t.Errorf("errOut=%q, want it to describe the missing led argument", errOut)
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

// Only the dry-run case above was ever tested - confirms the REAL
// (non-dry-run) exit code too: EX2's rtc write is "not implemented"
// (a planErr, exitUsage=1), semantically distinct from "not supported
// on this board" (exitUnsupported=2, e.g. EX2's display) even though
// both end up unable to do anything - "not implemented" is this
// project's own gap (a future RE pass could close it), while "not
// supported" is a hardware fact that will never change.
func TestRun_RTCWriteUnimplementedOnEX2_RealRun(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2", "-r", "write"})
	if code != exitUsage {
		t.Errorf("code=%d, want %d (exitUsage, distinct from exitUnsupported)", code, exitUsage)
	}
	if !strings.Contains(errOut, "not confirmed yet") {
		t.Errorf("errOut=%q, want it to mention EX2's RTC gap", errOut)
	}
}

// rtc.ErrEX2NotImplemented is exported specifically so a caller can
// check for it with errors.Is - a library consumer of this project's
// packages (not just this CLI) might reasonably do that, rather than
// matching its message text, since it's a well-known sentinel. That
// contract was never actually pinned anywhere: nothing in this
// codebase uses errors.Is/errors.As at all, and buildRTCAction's
// action.planErr field is only ever inspected as a string (via the
// CLI's own stdout/stderr) in every other test. Confirm the sentinel
// identity survives unwrapped all the way into action.planErr, so a
// future refactor can't silently start wrapping it (e.g.
// fmt.Errorf("rtc: %w", ...)) without a test catching that it broke
// errors.Is for anyone depending on it.
func TestBuildRTCAction_EX2_PlanErrIsErrEX2NotImplemented(t *testing.T) {
	act, code := buildRTCAction(board.EX2, "/dev/ttyS1", []string{"write"})
	if code != exitOK || act == nil {
		t.Fatalf("buildRTCAction(EX2, write): act=%v code=%d", act, code)
	}
	if !errors.Is(act.planErr, rtc.ErrEX2NotImplemented) {
		t.Errorf("planErr = %v, want errors.Is(planErr, rtc.ErrEX2NotImplemented) to hold", act.planErr)
	}
}

func TestRun_RTCUnsupportedOnEX2U(t *testing.T) {
	code, _, _ := captureRun(t, []string{"-b", "ex2u", "-n", "-r", "write"})
	if code != exitUnsupported {
		t.Errorf("code=%d, want %d", code, exitUnsupported)
	}
}

// Regression test: buildRTCAction used to capture `now` once for the
// -D debug preview but call time.Now() a SECOND time inside apply()
// for the actual write - so a real run's frame could carry different
// BCD seconds/minutes than what -D had just printed, breaking -D's
// own documented promise ("the exact frame bytes that would go out
// the UART"). Run a REAL (non-dry-run) rtc write against a fake MCU
// with -D on, and assert the bytes actually sent over the wire are
// byte-for-byte identical to the "frame=..." debug line printed for
// that exact same action - not just "close enough" modulo a clock
// tick, which a timing-based assertion could flakily let slide.
func TestRun_RTCWrite_RealRunSendsExactlyTheFrameDebugPrinted(t *testing.T) {
	fake := fakemcu.New(t)

	code, out, errOut := captureRun(t, []string{"-b", "ex4", "--serial", fake.SlavePath, "-D", "-r", "write"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}

	const marker = "frame="
	idx := strings.Index(out, marker)
	if idx == -1 {
		t.Fatalf("out=%q, want a debug line with %q", out, marker)
	}
	hexPart := out[idx+len(marker):]
	if nl := strings.IndexByte(hexPart, '\n'); nl != -1 {
		hexPart = hexPart[:nl]
	}
	var wantFrame []byte
	for _, tok := range strings.Fields(hexPart) {
		b, err := strconv.ParseUint(tok, 16, 8)
		if err != nil {
			t.Fatalf("parsing debug frame byte %q (from %q): %v", tok, hexPart, err)
		}
		wantFrame = append(wantFrame, byte(b))
	}
	if len(wantFrame) != 13 {
		t.Fatalf("parsed debug frame = % X (%d bytes), want 13 (WDateAndTime's fixed frame size)", wantFrame, len(wantFrame))
	}

	gotFrame := fake.ReadFrame(t, 13, 2*time.Second)
	if !bytes.Equal(gotFrame, wantFrame) {
		t.Errorf("actual wire frame = % X, want exactly the debug-printed frame % X (apply() must reuse the same timestamp debug used, not call time.Now() again)", gotFrame, wantFrame)
	}
}

// Same property as TestRun_RTCWrite_RealRunSendsExactlyTheFrameDebugPrinted,
// but for TWO chained rtc actions in one command: confirms each gets
// its own independently-computed timestamp (buildRTCAction is called
// once per pending action, so there's no risk of one action's `now`
// leaking into the other's), and that both frames arrive on the wire
// in order, each exactly matching ITS OWN debug-printed frame.
func TestRun_RTCWrite_TwoChainedActions_EachSendsItsOwnDebugPrintedFrame(t *testing.T) {
	fake := fakemcu.New(t)

	code, out, errOut := captureRun(t, []string{"-b", "ex4", "--serial", fake.SlavePath, "-D", "-r", "write", "-r", "write"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}

	var wantFrames [][]byte
	rest := out
	for {
		idx := strings.Index(rest, "frame=")
		if idx == -1 {
			break
		}
		hexPart := rest[idx+len("frame="):]
		if nl := strings.IndexByte(hexPart, '\n'); nl != -1 {
			hexPart = hexPart[:nl]
		}
		var frame []byte
		for _, tok := range strings.Fields(hexPart) {
			b, err := strconv.ParseUint(tok, 16, 8)
			if err != nil {
				t.Fatalf("parsing debug frame byte %q: %v", tok, err)
			}
			frame = append(frame, byte(b))
		}
		wantFrames = append(wantFrames, frame)
		rest = rest[idx+len("frame="):]
	}
	if len(wantFrames) != 2 {
		t.Fatalf("out=%q, want exactly 2 \"frame=\" debug lines, got %d", out, len(wantFrames))
	}

	for i, want := range wantFrames {
		if len(want) != 13 {
			t.Fatalf("frame %d = % X (%d bytes), want 13", i, want, len(want))
		}
		got := fake.ReadFrame(t, 13, 2*time.Second)
		if !bytes.Equal(got, want) {
			t.Errorf("frame %d: wire bytes = % X, want exactly its own debug-printed frame % X", i, got, want)
		}
	}
}

// Unlike rtc's fixed 13-byte frame or led's fixed 7-byte frame,
// display frames are VARIABLE length (LEN byte + text). Confirms two
// chained display actions with DIFFERENT-length texts each produce
// exactly the right number of bytes on the wire, read back in order
// with no byte-boundary confusion between the two frames.
func TestRun_RealRun_TwoChainedDisplayActions_DifferentLengths(t *testing.T) {
	fake := fakemcu.New(t)

	code, _, errOut := captureRun(t, []string{"-b", "ex4", "--serial", fake.SlavePath, "-d", "Hi", "-d", "Hello"})
	if code != exitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}

	frame1 := fake.ReadFrame(t, 8, 2*time.Second) // FA 04 01 00 02 'H' 'i' FB
	want1 := []byte{0xFA, 0x04, 0x01, 0x00, 0x02, 'H', 'i', 0xFB}
	if !bytes.Equal(frame1, want1) {
		t.Errorf("1st frame = % X, want % X", frame1, want1)
	}

	frame2 := fake.ReadFrame(t, 11, 2*time.Second) // FA 04 01 00 05 'H' 'e' 'l' 'l' 'o' FB
	want2 := []byte{0xFA, 0x04, 0x01, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o', 0xFB}
	if !bytes.Equal(frame2, want2) {
		t.Errorf("2nd frame = % X, want % X", frame2, want2)
	}
}

// Every existing EX2U real-run test exercises exactly one action
// (fan alone, or led alone - see
// TestRun_RealRun_EX2ULed_SendsCorrectFrameForEachState). Confirms
// TWO chained EX2U actions (fan then led) BOTH succeed for real and
// BOTH produce exactly the right frame on the wire, in order.
func TestRun_RealRun_EX2U_FanThenLed_BothSucceed(t *testing.T) {
	fake := fakemcu.New(t)

	code, _, errOut := captureRun(t, []string{"-b", "ex2u", "--serial", fake.SlavePath, "-f", "25", "-l", "on"})
	if code != exitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}

	fanFrame := fake.ReadFrame(t, 7, 3*time.Second)
	wantFan := []byte{0xFA, 0x02, 0x00, 0x40, 0x00, 0x00, 0xFB} // 25% = 0x40
	if !bytes.Equal(fanFrame, wantFan) {
		t.Errorf("1st frame (fan) = % X, want % X", fanFrame, wantFan)
	}

	ledFrame := fake.ReadFrame(t, 7, 3*time.Second)
	wantLed := []byte{0xFA, 0x03, 0x06, 0x01, 0x00, 0x00, 0xFB} // led on
	if !bytes.Equal(ledFrame, wantLed) {
		t.Errorf("2nd frame (led) = % X, want % X", ledFrame, wantLed)
	}
}

func TestRun_DryRun_EX2UTemp(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2u", "-n", "-D", "-t"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "FA 03 08 00 00 00 FB") {
		t.Errorf("out=%q, want the tmp_get request frame", out)
	}
	if !strings.Contains(out, "would request temperature") {
		t.Errorf("out=%q, missing the dry-run description line", out)
	}
}

func TestRun_Temp_UnsupportedOnOtherBoards(t *testing.T) {
	for _, b := range []string{"ex2", "ex4"} {
		code, _, errOut := captureRun(t, []string{"-b", b, "-t"})
		if code != exitUnsupported {
			t.Errorf("board %s: code=%d, want %d; err=%q", b, code, exitUnsupported, errOut)
		}
		if !strings.Contains(errOut, "temp is not supported") {
			t.Errorf("board %s: errOut=%q", b, errOut)
		}
	}
}

// Drives buildTempAction's real apply() directly (not through
// captureRun/run()) so the fake MCU's reply can be written WHILE
// apply() is blocked waiting for it - captureRun calls run()
// synchronously in the calling goroutine, so a test needing to
// interleave fakemcu I/O with the call under test can't use it from
// a background goroutine without calling *testing.T methods there,
// which Go's testing package disallows (FailNow/Fatal must run on
// the test's own goroutine). Running a.apply() (no *testing.T calls
// inside it) in the background goroutine instead, with the fakemcu
// interaction kept on the main test goroutine, avoids that entirely.
//
// Confirms the DEFAULT (non-debug) output for a reply that doesn't
// match the confirmed frame shape: no raw bytes (that's what -D is
// for - see TestRun_RealRun_EX2UTemp_DebugPrintsRawReplyBytes), just
// a plain message pointing at -D, and no decoded Temperature line.
func TestRun_RealRun_EX2UTemp_UndecodableReplyPrintsPlainMessage(t *testing.T) {
	fake := fakemcu.New(t)

	a, code := buildTempAction(board.EX2U, fake.SlavePath, nil, false)
	if code != exitOK || a == nil {
		t.Fatalf("buildTempAction: code=%d", code)
	}

	origOut := os.Stdout
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = outW
	outCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()

	applyDone := make(chan error, 1)
	go func() { applyDone <- a.apply() }()

	fake.ReadFrame(t, 7, 3*time.Second) // drain the tmp_get request
	if _, err := fake.Master.Write([]byte{0x11, 0x22, 0x33}); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	if err := <-applyDone; err != nil {
		t.Fatalf("apply: %v", err)
	}
	outW.Close()
	os.Stdout = origOut
	out := <-outCh

	if strings.Contains(out, "11 22 33") {
		t.Errorf("stdout=%q, raw bytes must not be printed without -D", out)
	}
	if !strings.Contains(out, "-D") {
		t.Errorf("stdout=%q, want the plain message to point at -D", out)
	}
	if strings.Contains(out, "Temperature:") {
		t.Errorf("stdout=%q, a reply that doesn't match the confirmed frame shape must not print a decoded Temperature line", out)
	}
}

// Same undecodable reply, but with -D: confirms the raw bytes now
// show up as a single "debug: reply=..." line (matching the existing
// "debug: frame=..." request-side line's style - not a second,
// differently-worded line), and ONLY as that one line, not duplicated
// anywhere else in the output.
func TestRun_RealRun_EX2UTemp_DebugPrintsRawReplyBytes(t *testing.T) {
	fake := fakemcu.New(t)

	a, code := buildTempAction(board.EX2U, fake.SlavePath, nil, true)
	if code != exitOK || a == nil {
		t.Fatalf("buildTempAction: code=%d", code)
	}

	origOut := os.Stdout
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = outW
	outCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()

	applyDone := make(chan error, 1)
	go func() { applyDone <- a.apply() }()

	fake.ReadFrame(t, 7, 3*time.Second) // drain the tmp_get request
	if _, err := fake.Master.Write([]byte{0x11, 0x22, 0x33}); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	if err := <-applyDone; err != nil {
		t.Fatalf("apply: %v", err)
	}
	outW.Close()
	os.Stdout = origOut
	out := <-outCh

	if !strings.Contains(out, "debug: reply=11 22 33") {
		t.Errorf("stdout=%q, want a single \"debug: reply=...\" line with the raw bytes", out)
	}
	if n := strings.Count(out, "11 22 33"); n != 1 {
		t.Errorf("stdout=%q, raw bytes appeared %d times, want exactly 1 (no duplication)", out, n)
	}
}

// End-to-end CLI version of TestEX2UDecodeTempCelsius_RealCapturedReply:
// feeds the exact bytes captured on real EX2U hardware and confirms
// the default (non-debug) output is exactly the decoded
// "Temperature: 30 C" line, matching the vendor tmp_get_c's own
// wording - with no raw-bytes line alongside it.
func TestRun_RealRun_EX2UTemp_DecodesRealCapturedReply(t *testing.T) {
	fake := fakemcu.New(t)

	a, code := buildTempAction(board.EX2U, fake.SlavePath, nil, false)
	if code != exitOK || a == nil {
		t.Fatalf("buildTempAction: code=%d", code)
	}

	origOut := os.Stdout
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = outW
	outCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()

	applyDone := make(chan error, 1)
	go func() { applyDone <- a.apply() }()

	fake.ReadFrame(t, 7, 3*time.Second) // drain the tmp_get request
	realCapturedReply := []byte{0xFA, 0x03, 0x08, 0x00, 0x00, 0x56, 0xFB, 0xFA}
	if _, err := fake.Master.Write(realCapturedReply); err != nil {
		t.Fatalf("write fake reply: %v", err)
	}

	if err := <-applyDone; err != nil {
		t.Fatalf("apply: %v", err)
	}
	outW.Close()
	os.Stdout = origOut
	out := <-outCh

	if strings.Contains(out, "FA 03 08 00 00 56 FB FA") {
		t.Errorf("stdout=%q, raw reply bytes must not be printed without -D", out)
	}
	if !strings.Contains(out, "Temperature: 30 C") {
		t.Errorf("stdout=%q, want the decoded \"Temperature: 30 C\" line", out)
	}
}

// Mirrors TestRun_RealRun_EX2UTemp_PrintsRawReplyBytes but for the
// outcome every real capture of this command has actually shown so
// far: nothing comes back. Confirms apply() reports that plainly
// (and still returns a nil error - a timeout here is expected, not a
// failure) rather than hanging past ex2uTempReplyTimeout.
func TestRun_RealRun_EX2UTemp_NoReplyPrintsNoReplyMessage(t *testing.T) {
	fake := fakemcu.New(t)

	a, code := buildTempAction(board.EX2U, fake.SlavePath, nil, false)
	if code != exitOK || a == nil {
		t.Fatalf("buildTempAction: code=%d", code)
	}

	origOut := os.Stdout
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = outW
	outCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()

	applyDone := make(chan error, 1)
	go func() { applyDone <- a.apply() }()

	fake.ReadFrame(t, 7, 3*time.Second) // drain the request, send nothing back

	if err := <-applyDone; err != nil { // blocks ~ex2uTempReplyTimeout
		t.Fatalf("apply: %v", err)
	}
	outW.Close()
	os.Stdout = origOut
	out := <-outCh

	if !strings.Contains(out, "no reply") {
		t.Errorf("stdout=%q, want a \"no reply\" message", out)
	}
}

func TestRun_RealRun_HardwareIOErrorOnBadSerialPath(t *testing.T) {
	// A path that cannot exist makes mcu.Open fail at os.OpenFile,
	// before any real device is touched - a safe way to exercise the
	// exitHardwareIO path without hardware.
	bad := "/nonexistent/wd-control-tool-test-path"
	cases := [][]string{
		{"-b", "ex2u", "--serial", bad, "-f", "50"},
		{"-b", "ex4", "--serial", bad, "-f", "2"},   // found missing via go tool cover: EX4's fan apply() had no coverage of mcu.Open's error branch at all
		{"-b", "ex2u", "--serial", bad, "-l", "on"}, // same gap, EX2U's led apply()
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

// EX2U's own led vocabulary (a single STATE argument, not EX4's
// zone/color/state scheme) has its own alias pairs - "blinking"/
// "blink" and "breathing"/"breathe" - fully exercised at the
// mcu.EX2ULEDFrame level (pkg/mcu/ex2u_test.go), but never all
// together through the actual CLI dispatch path
// (buildLEDAction -> run()). Close that the same way
// TestRun_LEDAllZonesColorsStates closes it for EX4.
func TestRun_LEDAllStates_EX2U(t *testing.T) {
	for _, state := range []string{"on", "off", "blinking", "blink", "breathing", "breathe"} {
		code, out, errOut := captureRun(t, []string{"-b", "ex2u", "-n", "-l", state})
		if code != exitOK || !strings.Contains(out, "would") || strings.Contains(out, "would fail") {
			t.Errorf("state=%q: code=%d out=%q err=%q", state, code, out, errOut)
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

// Regression test for a bug the -b-look-alike-argument fix itself
// introduced (caught by actually running the command, not just
// reading the diff): `-l on -b ex2u` is a perfectly ordinary command
// - EX2U's single-token led state, then the board, in the order
// "-b may appear anywhere" explicitly promises is fine (see
// TestParseArgs_BoardAnywhereBeforeModeFlags) - but the fix's
// conservative "assume -l takes 3 arguments when board isn't known
// yet" guess, applied here, skipped 3 tokens ("on","-b","ex2u")
// instead of EX2U's real 1 ("on"), eating the genuine -b/--board
// flag along with it and failing with "-b/--board is required".
// Fixed by adding a separate, non-authoritative hint pass (which
// doesn't try to skip anything, the same way the original pre-fix
// code worked) used ONLY to size this one skip correctly ahead of
// time, while the real, skip-aware pass remains authoritative for
// everything else - including the original bug this whole thing
// started from, covered by TestRun_ActionArgumentThatLooksLikeBoardFlag.
func TestRun_LedBeforeBoard_EX2U_SingleArgArity(t *testing.T) {
	code, out, errOut := captureRun(t, []string{"-l", "on", "-b", "ex2u", "-n"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "led on to EX2U") {
		t.Errorf("out=%q, want the led action described", out)
	}
}

// A stronger variant of the above: TWO EX2U led actions chained
// before the board flag, plus a fan action for good measure -
// confirms the hint pass's "board not yet known at THIS point in
// the real pass's own scan" fallback keeps working correctly across
// multiple occurrences, not just a lone one, and alongside another
// action type's own fixed-arity argument.
func TestRun_MultipleActionsBeforeBoard_EX2U(t *testing.T) {
	code, out, errOut := captureRun(t, []string{"-f", "50", "-l", "on", "-l", "off", "-b", "ex2u", "-n"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "fan 50%") || !strings.Contains(out, "led on to EX2U") || !strings.Contains(out, "led off to EX2U") {
		t.Errorf("out=%q, want all 3 actions described", out)
	}
}

// Regression test for a subtler instance of the same hint-pass bug
// class: `-l on -b ex2u -b ex4` has led BEFORE two CONFLICTING
// -b/--board occurrences. An earlier version of the hint pass kept
// the LAST value it saw (ex4) rather than the first (ex2u), sizing
// -l's arity for ex4 (3 tokens) instead of the genuinely-first
// ex2u's real arity (1) - which swallowed the real "-b ex2u" into
// led's own (mis-sized) argument span, so the real pass never saw
// it as a board flag at all and the conflict check never fired:
// instead of a clear "conflicts with an earlier -b", this failed
// with a confusing "unknown led zone \"on\"" (led's own 3 assumed
// arguments ended up being "on", "-b", "ex2u" - garbage zone/color/
// state). Fixed by having the hint keep the first value, matching
// setBoard's own "first occurrence wins" semantics.
func TestRun_LedBeforeConflictingBoards_EX2U(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-l", "on", "-b", "ex2u", "-b", "ex4", "-n"})
	if code != exitUsage {
		t.Fatalf("code=%d errOut=%q, want exitUsage", code, errOut)
	}
	if !strings.Contains(errOut, "conflicts") {
		t.Errorf("errOut=%q, want it to clearly report the board conflict, not a confusing led-validation error", errOut)
	}
}

// The hint pass's arity decision compares against board.EX2U (the
// resolved Board, via board.Parse), never the raw -b/--board string
// - so an ALIAS of EX2U ("ex2-ultra", not the literal "ex2u") must
// resolve the hint just as correctly as the canonical spelling does.
func TestRun_LedBeforeBoard_EX2U_ViaAlias(t *testing.T) {
	code, out, errOut := captureRun(t, []string{"-l", "on", "-b", "ex2-ultra", "-n"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "led on to EX2U") {
		t.Errorf("out=%q, want the led action described", out)
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
		"-t": "temp", "--temp": "temp",
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
		{"temp", board.EX2U, 0},
	}
	for _, c := range cases {
		if got := arityFor(c.mode, c.b); got != c.want {
			t.Errorf("arityFor(%q, %v) = %d, want %d", c.mode, c.b, got, c.want)
		}
	}
}

// arityFor's default case (return 0) is unreachable-in-practice the
// same way buildAction's mode panic and buildFanAction/
// buildLEDAction's unsupported-board branches are - modeNameFor only
// ever returns "fan"/"led"/"display"/"rtc"/"temp" (or ""), and arityFor is
// only ever called when that's non-empty. Closes the matching gap
// for arityFor's own defensive branch, same as those three.
func TestArityFor_UnknownModeReturnsZero(t *testing.T) {
	if got := arityFor("bogus", board.EX4); got != 0 {
		t.Errorf("arityFor(%q, ...) = %d, want 0", "bogus", got)
	}
}

// buildAction's default case is an unreachable-in-practice panic:
// parseArgs only ever calls it with a mode string that came from
// modeNameFor, which only ever returns "fan"/"led"/"display"/"rtc"/"temp"
// (or ""), so every real caller already has a valid mode by
// construction. "Unreachable in practice" isn't the same as
// "unreachable in fact" - if a future edit to parseArgs/modeNameFor
// ever let a different string through, this is the loud, documented
// invariant that should catch it rather than silently misbehave.
// Confirm it actually panics (with a useful message), by calling it
// directly - same package, so this doesn't need parseArgs' own
// guarantees to hold.
func TestBuildAction_PanicsOnUnknownMode(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("buildAction(..., \"bogus\", ...): want a panic, got none")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, "bogus") {
			t.Errorf("panic value = %v, want a message mentioning the unknown mode", r)
		}
	}()
	buildAction(board.EX4, "bogus", "/dev/ttyS1", nil, false)
}

// buildFanAction/buildLEDAction's own "unsupported board" default
// branches are just as unreachable in practice as buildAction's mode
// panic (board.Parse only ever returns EX2U/EX2/EX4), and found via
// the same `go tool cover` pass that caught
// TestRun_RealRun_EX2ULed_SendsCorrectFrameForEachState's gap. Call
// them directly with board.Unknown to confirm the defensive path
// itself behaves correctly (returns nil/exitUnsupported, doesn't
// panic) rather than leaving it completely unexercised.
func TestBuildFanAction_UnsupportedBoardReturnsUnsupportedErr(t *testing.T) {
	origErr := os.Stderr
	_, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = origErr; w.Close() }()

	act, code := buildFanAction(board.Unknown, "/dev/ttyS1", []string{"1"})
	if act != nil || code != exitUnsupported {
		t.Errorf("buildFanAction(Unknown, ...) = (%v, %d), want (nil, %d)", act, code, exitUnsupported)
	}
}

func TestBuildLEDAction_UnsupportedBoardReturnsUnsupportedErr(t *testing.T) {
	origErr := os.Stderr
	_, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = origErr; w.Close() }()

	act, code := buildLEDAction(board.Unknown, "/dev/ttyS1", []string{"power", "blue", "on"})
	if act != nil || code != exitUnsupported {
		t.Errorf("buildLEDAction(Unknown, ...) = (%v, %d), want (nil, %d)", act, code, exitUnsupported)
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

// devNullOnce lazily opens /dev/null once and reuses it across every
// FuzzRun_NeverPanics iteration - unlike captureRun's real os.Pipe +
// goroutine setup (built for inspecting output, which this fuzz
// target doesn't need), a shared /dev/null sink keeps each iteration
// cheap, which matters when the fuzzer wants to run many iterations
// per second.
var (
	devNullOnce sync.Once
	devNull     *os.File
)

func devNullFile(t *testing.T) *os.File {
	devNullOnce.Do(func() {
		f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("open %s: %v", os.DevNull, err)
		}
		devNull = f
	})
	return devNull
}

// Every other fuzz target in this project exercises one function deep
// in the call graph (a frame builder, a Plan, parseArgs' own
// tokenizer); none exercised run() itself, so buildLEDAction/
// buildFanAction/buildDisplayAction/buildRTCAction and the
// parseZone/parseColor/parseState/strconv.Atoi calls they make were
// never fuzzed at all - only ever driven by hand-picked test cases.
// -n/--dry-run is forced (prepended, regardless of what the fuzzer
// generates) specifically so a randomly-valid board+action
// combination can never reach real hardware I/O, matching every
// other test in this file (see captureRun's own doc comment) while
// still exercising the full dispatch path for an otherwise-arbitrary
// command line. Run with
// `go test -fuzz=FuzzRun_NeverPanics ./cmd/wd-control-tool` to fuzz
// beyond the seed corpus.
func FuzzRun_NeverPanics(f *testing.F) {
	for _, seed := range []string{
		"-b ex4 -f 3",
		"-b ex2u --led breathing",
		"-b ex2 --led power blue on -f 1",
		"-b ex4 --display hello",
		"-b ex4 -r write",
		"-b ex2u -f 50 -b ex2-ultra -f 25",
		"-b ex2u -f 50 -b ex4 -f 2",
		"-b ex2 -d hi",
		"-b bogus -f 1",
		"--led power blue on",
		"-h",
		"-v",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		args := append([]string{"-n"}, strings.Fields(s)...)

		null := devNullFile(t)
		origOut, origErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = null, null
		defer func() {
			os.Stdout, os.Stderr = origOut, origErr
			if r := recover(); r != nil {
				t.Fatalf("run(%q) panicked: %v", args, r)
			}
		}()
		run(args)
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

// The above puts -b first, so it never exercises the hint pass at
// all (board is already known before any action is scanned). This
// is the hint-pass's own stress test: many EX2U led actions BEFORE
// -b/--board, confirming both correctness (every action still
// described) and that the hint pass doesn't introduce any
// quadratic-ish slowdown at scale - verified by hand first with a
// real build at 200 and 1000 actions (58ms either way, confirming
// linear scaling, not just "fast enough at 200").
func TestRun_LongActionChainBeforeBoard_EX2U(t *testing.T) {
	const n = 200
	var args []string
	for i := 0; i < n; i++ {
		args = append(args, "-l", "on")
	}
	args = append(args, "-b", "ex2u", "-n")

	start := time.Now()
	code, out, errOut := captureRun(t, args)
	elapsed := time.Since(start)

	if code != exitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if got := strings.Count(out, "led on to EX2U"); got != n {
		t.Errorf("got %d described actions, want %d", got, n)
	}
	if elapsed > 2*time.Second {
		t.Errorf("200 dry-run actions took %v, want well under 2s", elapsed)
	}
}

// Every other multi-action test combines at most 2 of the 4 action
// types. Chain all 4 (led, fan, display, rtc) in one EX4 command and
// confirm each is correctly described, in order, with the right
// frame/register detail - the realistic "set everything up on boot"
// use case this feature exists for.
func TestRun_AllFourActionTypesChained(t *testing.T) {
	code, out, errOut := captureRun(t, []string{
		"-b", "ex4", "-n", "-D",
		"--led", "power", "blue", "on",
		"-f", "2",
		"-d", "Booting...",
		"-r", "write",
	})
	if code != exitOK {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}

	ledIdx := strings.Index(out, "set ex4 power blue on")
	fanIdx := strings.Index(out, "send fan level 2")
	dispIdx := strings.Index(out, `display text "Booting..."`)
	rtcIdx := strings.Index(out, "MCU RTC")
	if ledIdx == -1 || fanIdx == -1 || dispIdx == -1 || rtcIdx == -1 {
		t.Fatalf("out=%q, missing one of the 4 expected action descriptions", out)
	}
	if !(ledIdx < fanIdx && fanIdx < dispIdx && dispIdx < rtcIdx) {
		t.Errorf("actions not described in the order given: led=%d fan=%d display=%d rtc=%d", ledIdx, fanIdx, dispIdx, rtcIdx)
	}
	if !strings.Contains(out, "FA 02 00 02 00 00 FB") { // confirmed FanControl level-2 frame
		t.Errorf("out=%q, missing the fan frame bytes", out)
	}
}

// TestRun_AllFourActionTypesChained (above) only ever ran under
// -n/--dry-run, so it checks the TEXT -D prints, never bytes that
// actually went anywhere - and every "chain of several actions"
// test touching real I/O (e.g.
// TestRun_MultipleActions_EarlierActionAlreadyAppliedBeforeLaterUnsupportedStopsChain)
// stops after its first action specifically BECAUSE a later one
// fails. Nothing confirmed that a chain of several VALID,
// correctly-syntaxed actions actually applies every single one of
// them for real, in the order given, with the right bytes each time
// - the positive counterpart to
// TestRun_ChainWithTrailingSyntaxError_AbortsBeforeAnyActionRuns.
//
// EX4's fan/display/rtc actions all go over the MCU/UART path, which
// --serial lets a test redirect to a fakemcu pty; EX4's led action
// goes over real /dev/mem instead (no CLI override exists for that -
// see BUILDING.md on why no automated test opens a real /dev/mem),
// so led is intentionally left out of this real-run chain.
func TestRun_RealRun_MultipleValidActionsAllApplyCorrectlyInOrder(t *testing.T) {
	fake := fakemcu.New(t)

	code, _, errOut := captureRun(t, []string{"-b", "ex4", "--serial", fake.SlavePath, "-f", "2", "-d", "Hi", "-r", "write"})
	if code != exitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}

	fanFrame := fake.ReadFrame(t, 7, 2*time.Second)
	wantFan := []byte{0xFA, 0x02, 0x00, 0x02, 0x00, 0x00, 0xFB}
	if !bytes.Equal(fanFrame, wantFan) {
		t.Errorf("1st frame (fan) = % X, want % X", fanFrame, wantFan)
	}

	dispFrame := fake.ReadFrame(t, 8, 2*time.Second) // FA 04 01 00 <LEN=2> 'H' 'i' FB
	wantDisp := []byte{0xFA, 0x04, 0x01, 0x00, 0x02, 'H', 'i', 0xFB}
	if !bytes.Equal(dispFrame, wantDisp) {
		t.Errorf("2nd frame (display) = % X, want % X", dispFrame, wantDisp)
	}

	rtcFrame := fake.ReadFrame(t, 13, 2*time.Second) // WDateAndTime's fixed 13-byte frame
	if rtcFrame[0] != 0xFA || rtcFrame[12] != 0xFB {
		t.Errorf("3rd frame (rtc) = % X, want start/end markers FA/FB", rtcFrame)
	}
	if rtcFrame[1] != 0x01 || rtcFrame[2] != 0x00 || rtcFrame[3] != 0x04 || rtcFrame[4] != 0x07 {
		t.Errorf("3rd frame (rtc) header = % X, want WDateAndTime's 01 00 04 07", rtcFrame[1:5])
	}
}

// Found via `go tool cover`: buildLEDAction's EX2U branch's apply()
// closure - the real (non-dry-run) code path from mcu.Open through
// mcu.NewEX2U(p).LEDOn/LEDOff/LEDBlink/LEDBreathe - had ZERO
// coverage. Every other EX2U led test either used -n/--dry-run
// (which never calls apply() at all) or called mcu.EX2ULEDFrame/
// NewEX2U's methods directly (bypassing the CLI layer entirely). A
// real `-l STATE` on EX2U, run through the actual CLI, had never
// once been exercised end to end.
// EX2's power LED is MCU-driven (NewPWRLed), not a /dev/mem GPIO
// poke like every other LED zone - see buildEX2PowerLEDAction. These
// 4 cases are the exact bytes captured on real EX2 hardware via a
// live strace of the vendor `led` tool (see
// mcu.EX2PowerLEDFrames's doc comment).
func TestRun_RealRun_EX2PowerLed_SendsCorrectFramesForEachCase(t *testing.T) {
	cases := []struct {
		color, state string
		want         [2][]byte
	}{
		{"blue", "on", [2][]byte{
			{0xFA, 0x26, 0x00, 0x04, 0x00, 0x01, 0xFB},
			{0xFA, 0x26, 0x00, 0x11, 0x00, 0x01, 0xFB},
		}},
		{"blue", "off", [2][]byte{
			{0xFA, 0x26, 0x00, 0x04, 0x00, 0x01, 0xFB},
			{0xFA, 0x26, 0x00, 0x01, 0x00, 0x01, 0xFB},
		}},
		{"red", "on", [2][]byte{
			{0xFA, 0x26, 0x00, 0x01, 0x00, 0x01, 0xFB},
			{0xFA, 0x26, 0x00, 0x14, 0x00, 0x01, 0xFB},
		}},
		{"red", "off", [2][]byte{
			{0xFA, 0x26, 0x00, 0x01, 0x00, 0x01, 0xFB},
			{0xFA, 0x26, 0x00, 0x04, 0x00, 0x01, 0xFB},
		}},
	}
	for _, c := range cases {
		fake := fakemcu.New(t)
		code, _, errOut := captureRun(t, []string{"-b", "ex2", "--serial", fake.SlavePath, "-l", "power", c.color, c.state})
		if code != exitOK {
			t.Fatalf("%s %s: code=%d err=%q", c.color, c.state, code, errOut)
		}
		got := fake.ReadFrame(t, 14, 2*time.Second)
		want := append(append([]byte{}, c.want[0]...), c.want[1]...)
		if !bytes.Equal(got, want) {
			t.Errorf("%s %s: wire bytes = % X, want % X", c.color, c.state, got, want)
		}
	}
}

// EX2 power LED blinking has no confirmed MCU frame - must be a
// clear, reported error (both as a real-run usage error and as a
// dry-run "would fail"), never silently guessed or dropped.
func TestRun_EX2PowerLedBlinking_Unconfirmed(t *testing.T) {
	code, _, errOut := captureRun(t, []string{"-b", "ex2", "-l", "power", "blue", "blinking"})
	if code != exitUsage {
		t.Errorf("real run: code=%d, want %d; err=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "blinking") || !strings.Contains(errOut, "no confirmed") {
		t.Errorf("errOut=%q, want it to mention blinking being unconfirmed", errOut)
	}

	code, out, _ := captureRun(t, []string{"-b", "ex2", "-n", "-l", "power", "blue", "blinking"})
	if code != exitOK {
		t.Errorf("dry run: code=%d, want %d (dry-run always exits 0)", code, exitOK)
	}
	if !strings.Contains(out, "would fail") {
		t.Errorf("dry run out=%q, want a \"would fail\" line", out)
	}
}

// EX2's OTHER LED zones (hd0/hd1) are untouched by the power-LED
// special case - still the /dev/mem GPIO path.
func TestRun_DryRun_EX2HDBayLed_StillUsesRegio(t *testing.T) {
	code, out, _ := captureRun(t, []string{"-b", "ex2", "-n", "-D", "-l", "hd0", "blue", "on"})
	if code != exitOK {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "/dev/mem") {
		t.Errorf("out=%q, want hd0 to still describe a /dev/mem write", out)
	}
	if strings.Contains(out, "MCU") {
		t.Errorf("out=%q, hd0 must not be routed through the MCU", out)
	}
}

func TestRun_RealRun_EX2ULed_SendsCorrectFrameForEachState(t *testing.T) {
	cases := []struct {
		state string
		want  byte // the frame's param byte - see SPEC.md's EX2U command table
	}{
		{"on", 0x01},
		{"off", 0x00},
		{"blinking", 0x02},
		{"breathing", 0x04},
	}
	for _, c := range cases {
		fake := fakemcu.New(t)
		code, _, errOut := captureRun(t, []string{"-b", "ex2u", "--serial", fake.SlavePath, "-l", c.state})
		if code != exitOK {
			t.Fatalf("state=%q: code=%d err=%q", c.state, code, errOut)
		}
		got := fake.ReadFrame(t, 7, 2*time.Second)
		want := []byte{0xFA, 0x03, 0x06, c.want, 0x00, 0x00, 0xFB}
		if !bytes.Equal(got, want) {
			t.Errorf("state=%q: wire bytes = % X, want % X", c.state, got, want)
		}
	}
}
