// Package fakeregio stands in for /dev/mem in tests, so pkg/led and
// pkg/fan's EX2 backend can be exercised without root or real
// hardware.
//
// It works because mmap on a regular file behaves the same as mmap
// on /dev/mem for any offset within the file's (possibly sparse)
// size: /dev/mem lets you map any physical page by treating the
// whole address space as the file's contents; a plain file mmaps the
// same way as long as it is at least as large as the highest
// offset+length you map. The register addresses this tool cares
// about (0xF1010100..0xF1018148) are all under 4GiB, so truncating a
// temp file up to a bit past the highest address used gives an
// mmap-compatible stand-in. The file is created sparse (ftruncate,
// not actually written), so this costs no real disk space - only the
// handful of 4KiB pages a test actually touches get allocated.
package fakeregio

import (
	"os"
	"testing"

	"github.com/symops/wd-control-tool/pkg/regio"
)

// highWaterMark covers every physical address pkg/led and pkg/fan
// currently use (0xF1010148, 0xF1018140) plus margin for future
// registers in the same window.
const highWaterMark = 0xF1030000

// New creates a sparse temp file sized to cover every register
// address this project uses, wraps it as a *regio.Mem via
// regio.OpenFile, and registers cleanup. The returned Mem's
// ReadReg32/WriteReg32/SetClearBits behave identically to the real
// /dev/mem-backed implementation - tests exercise the exact same
// code path production code does, only the backing file differs.
func New(t *testing.T) *regio.Mem {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "fakeregio-*")
	if err != nil {
		t.Fatalf("fakeregio: create temp file: %v", err)
	}
	if err := f.Truncate(highWaterMark); err != nil {
		t.Fatalf("fakeregio: truncate: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	return regio.OpenFile(f)
}
