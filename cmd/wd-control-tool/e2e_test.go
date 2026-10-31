package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Everything else in this package drives run()/parseArgs() in-process
// via captureRun - main() itself (os.Exit(run(os.Args[1:]))) and the
// real process boundary (actual argv, actual process exit code) had
// no coverage at all. Build the real binary once and exec it for a
// handful of representative cases, to close that specific gap rather
// than assume the one-line main() can't have its own bug (e.g. the
// wrong args being passed to run, or os.Exit not actually propagating
// run()'s return value as the process exit code).
var (
	e2eBinOnce sync.Once
	e2eBinPath string
	e2eBinErr  error
)

func buildE2EBinary(t *testing.T) string {
	t.Helper()
	e2eBinOnce.Do(func() {
		// Not t.TempDir(): that gets removed when the CALLING
		// subtest ends, but this binary is shared (built once) across
		// every test in the package via e2eBinOnce.
		dir := filepath.Join(os.TempDir(), "wd-control-tool-e2e-test-bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			e2eBinErr = err
			return
		}
		e2eBinPath = filepath.Join(dir, "wd-control-tool")
		cmd := exec.Command("go", "build", "-o", e2eBinPath, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			e2eBinErr = err
			t.Logf("build output: %s", out)
		}
	})
	if e2eBinErr != nil {
		t.Fatalf("build e2e binary: %v", e2eBinErr)
	}
	return e2eBinPath
}

func runE2E(t *testing.T, args ...string) (exitCode int, stdout, stderr string) {
	t.Helper()
	bin := buildE2EBinary(t)
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err == nil {
		return 0, outBuf.String(), errBuf.String()
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), outBuf.String(), errBuf.String()
	}
	t.Fatalf("run: %v", err)
	return -1, "", ""
}

func TestE2E_VersionExitsZero(t *testing.T) {
	code, out, _ := runE2E(t, "-v")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if out == "" {
		t.Error("expected version output on stdout, got none")
	}
}

func TestE2E_NoArgsExitsOne(t *testing.T) {
	code, _, errOut := runE2E(t)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if errOut == "" {
		t.Error("expected an error message on stderr, got none")
	}
}

func TestE2E_DryRunFanExitsZero(t *testing.T) {
	code, out, _ := runE2E(t, "-b", "ex2u", "-n", "-f", "50")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if out == "" {
		t.Error("expected dry-run output on stdout, got none")
	}
}

func TestE2E_UnsupportedBoardComboExitsTwo(t *testing.T) {
	code, _, _ := runE2E(t, "-b", "ex2", "-n", "-d", "hi")
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}
