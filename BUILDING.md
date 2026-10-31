# Building wd-control-tool

## Toolchain

- Go 1.22+.
- Cross C compilers (needed only so `CGO_ENABLED=1` + `-linkmode=external`
  produce a *dynamically*-linked binary against the target's own glibc,
  not a statically-linked Go binary - see the comment on `LDFLAGS` in
  the `Makefile` for why `CGO_ENABLED=1` alone isn't enough):
  - `arm-linux-gnueabihf-gcc` (+ `-strip`, `-readelf`) for armv7
    hard-float (EX2U, and EX2 once its uRamdisk is rebuilt).
  - `arm-linux-gnueabi-gcc` (+ `-strip`, `-readelf`) for armv5
    soft-float (EX4's current target; EX2's *current* uRamdisk also
    uses this ABI today, even though its real silicon is armv7).

  On Debian/Ubuntu:
  ```sh
  apt install gcc-arm-linux-gnueabihf gcc-arm-linux-gnueabi
  ```

## Clone and fetch dependencies

```sh
git clone https://github.com/symops/wd-control-tool
cd wd-control-tool
go mod tidy   # fetches golang.org/x/sys - the only non-stdlib dependency
```

## Build

```sh
make build-ex2u   # -> wd-control-tool-armv7
make build-ex4    # -> wd-control-tool-armv5
make verify-deps  # fails the build if either binary needs anything beyond libc.so.6
make strip        # verify-deps, then strip - this is what a release ships
```

`make build-host` builds a native `linux/amd64` binary for local
testing (argument parsing, `go test ./...`) - it cannot touch
`/dev/mem` or a real serial port meaningfully on a dev machine, so it
is not a substitute for testing on actual hardware.

Confirm the result is actually dynamically linked before shipping it -
this has silently regressed to a static binary before (see the
`LDFLAGS` comment in the `Makefile`):

```sh
file wd-control-tool-armv7      # expect: ...dynamically linked, interpreter /lib/ld-linux-armhf.so.3...
arm-linux-gnueabihf-readelf -d wd-control-tool-armv7 | grep NEEDED   # expect: only libc.so.6
```

## Test

```sh
make test    # go vet + go test ./...
```

Coverage, all without root or real hardware:

- Golden-byte unit tests for the frame-encoding functions
  (`pkg/mcu/*_test.go`), checked against the worked examples in
  `SPEC.md`.
- End-to-end transmit tests over a real Linux pty pair
  (`test/fakemcu`, used from `pkg/mcu/integration_test.go` and
  `pkg/fan/ex2u_test.go`): the tool's actual `Port.sendBytes` write
  loop and inter-byte pacing run unmodified, with the test reading
  back exactly what crossed the "wire". This is also what caught a
  real bug while writing it: an unconfigured pty slave defaults to
  cooked-mode line discipline, so any frame byte that happens to
  equal `\n` (0x0A) - which `SysName`'s own length byte does whenever
  the text is exactly 10 characters - got silently expanded to
  `\r\n`. Fixed by putting the slave in raw mode in `test/fakemcu`,
  matching what `mcu.Open` always does for a real UART.
- Register read/modify/write tests against a sparse temp file
  standing in for `/dev/mem` (`test/fakeregio`, used from
  `pkg/led/led_test.go`, `pkg/fan/ex2_test.go` and directly in
  `pkg/regio/regio_test.go`): `pkg/regio`'s real mmap-based code runs
  unmodified against the fake backing file. This caught a second real
  bug: `ReadReg32`/`WriteReg32` originally mmap'd exactly one page, so
  a register whose 4 bytes straddle a page boundary (nothing about a
  physical address guarantees it won't) paniced with an out-of-range
  slice index instead of working or returning an error. None of this
  tool's current registers happen to straddle a boundary, so this
  would only have bitten a future register address - fixed by always
  mapping 2 pages (see `mapRegister` in `pkg/regio/regio.go`).
- CLI-level tests (`cmd/wd-control-tool/main_test.go`) drive `run()`
  and `parseArgs()` directly, with stdout/stderr captured via
  `os.Pipe`. This caught a third real bug: `-h`/`--help` was writing
  its usage text to stderr instead of stdout.

Run `go test -race ./...` after touching any of the above - the pty
tests use goroutines (the command runs concurrently with the test
reading the "wire"), so a race there would be a real concurrency bug,
not a test artifact.

No automated test opens a real `/dev/mem` (only the sparse-file
stand-in) or sends traffic over a real UART to actual hardware - by
design, this suite proves the tool's own logic (frame bytes, register
math, CLI parsing/validation/exit codes) is correct, not that the
real MCU or SoC behaves as SPEC.md describes. That still needs real
hardware (see the project's "на реальном железе посмотим потом" note
- real-hardware verification is a separate, later step).

## What this does NOT cover

This repository builds only the `wd-control-tool` binary itself.
Packaging it into a board's rescue `uRamdisk` (busybox rootfs, init
script, cpio/gzip/mkimage) is a separate project and out of scope
here.

## Author

Symo <symops@gmail.com>
