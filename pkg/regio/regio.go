// Package regio provides direct physical-register read/modify/write
// access via /dev/mem - the mechanism EX2 and EX4's vendor libshare.so
// used (LIB_Read_Mem/LIB_Write_Mem) for LED control on both boards,
// and for fan-speed control on EX2 specifically. See SPEC.md,
// "Register-mapped hardware (EX2/EX4)".
//
// This is a clean-room reimplementation of the same open64+mmap64
// pattern confirmed in libshare.so's disassembly - no vendor code is
// linked or reused.
package regio

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// Mem is an open /dev/mem handle with one page mapped at a time.
// Safe for concurrent use from multiple goroutines: every method
// (including Close) takes mu, so calls serialize rather than race -
// not that this project's own CLI ever shares one Mem across
// goroutines (each action opens and closes its own), but this type
// is exported and nothing stops a future caller from doing so.
type Mem struct {
	mu sync.Mutex
	f  *os.File
}

// Open opens /dev/mem for register access. Requires root (or
// CAP_SYS_RAWIO).
func Open() (*Mem, error) {
	f, err := os.OpenFile("/dev/mem", os.O_RDWR|unix.O_SYNC, 0)
	if err != nil {
		return nil, fmt.Errorf("regio: open /dev/mem: %w", err)
	}
	return &Mem{f: f}, nil
}

// OpenFile wraps an already-open file as a Mem without requiring it
// to be /dev/mem - used by test/fakeregio so tests can exercise this
// exact mmap-based read/modify/write code against a sparse regular
// file standing in for physical memory (see test/fakeregio's doc
// comment for why a regular file works at all), and generally useful
// for pointing this tool at a saved memory-dump file for offline
// inspection (the same reasoning verona-tool documents for accepting
// a plain file in place of a block device).
func OpenFile(f *os.File) *Mem { return &Mem{f: f} }

func (m *Mem) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.f.Close()
}

func pageAlign(addr int64, pageSize int64) (base int64, off int64) {
	base = addr &^ (pageSize - 1)
	off = addr - base
	return
}

// mapRegister mmaps enough pages to safely access the 4 bytes at
// addr and returns the mapping plus addr's offset within it. addr is
// not guaranteed to be far from a page boundary - nothing about a
// physical register's address implies that - so a register's 4 bytes
// can straddle into the next page. off (addr's offset within a single
// page) is always in [0, pageSize), so off+4 never exceeds
// pageSize+3: mapping 2 pages unconditionally is always enough and
// keeps this simple, at the cost of mapping one page more than
// strictly needed in the common case. A single-page mmap here was
// this function's original design and would panic with an
// out-of-range slice index for a straddling register - caught by
// pkg/regio_test.go's TestReadWriteReg32_PageBoundaryStraddle.
func (m *Mem) mapRegister(addr int64) (data []byte, off int64, cleanup func(), err error) {
	pageSize := int64(os.Getpagesize())
	base, off := pageAlign(addr, pageSize)

	data, err = unix.Mmap(int(m.f.Fd()), base, int(2*pageSize), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("regio: mmap 0x%x: %w", addr, err)
	}
	return data, off, func() { unix.Munmap(data) }, nil
}

// ReadReg32 reads one little-endian 32-bit word at physical address
// addr.
func (m *Mem) ReadReg32(addr int64) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, off, cleanup, err := m.mapRegister(addr)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	v := uint32(data[off]) | uint32(data[off+1])<<8 | uint32(data[off+2])<<16 | uint32(data[off+3])<<24
	return v, nil
}

// WriteReg32 writes one little-endian 32-bit word at physical address
// addr.
func (m *Mem) WriteReg32(addr int64, v uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, off, cleanup, err := m.mapRegister(addr)
	if err != nil {
		return err
	}
	defer cleanup()

	data[off] = byte(v)
	data[off+1] = byte(v >> 8)
	data[off+2] = byte(v >> 16)
	data[off+3] = byte(v >> 24)
	return nil
}

// SetClearBits does a read-modify-write: set every bit in set, then
// clear every bit in clear (set is applied first, matching the
// vendor LED handlers' own "clear the other color, then set/clear
// mine" ordering - see SPEC.md).
func (m *Mem) SetClearBits(addr int64, set, clear uint32) error {
	v, err := m.ReadReg32(addr)
	if err != nil {
		return err
	}
	v |= set
	v &^= clear
	return m.WriteReg32(addr, v)
}

// Write is one planned register read-modify-write - the pure,
// no-I/O half of a register operation, so callers (the CLI's
// -n/--dry-run and -D/--debug) can describe what would happen before
// (or without) touching /dev/mem. See pkg/led.Plan and
// pkg/fan.Ex2FanPlan, which build these; Apply is the only thing that
// actually performs one.
type Write struct {
	Addr       int64
	Set, Clear uint32
}

// Apply performs one planned Write.
func (m *Mem) Apply(w Write) error {
	return m.SetClearBits(w.Addr, w.Set, w.Clear)
}

// ApplyAll performs a sequence of planned Writes in order, stopping
// at the first error.
func (m *Mem) ApplyAll(ws []Write) error {
	for _, w := range ws {
		if err := m.Apply(w); err != nil {
			return err
		}
	}
	return nil
}
