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
make test        # go vet + go test ./...
make test-race   # go vet + go test -race ./... - run before every push, not just this
make fuzz        # actually fuzz (not just the seed corpus `test` runs) - 30s per target by default, `make fuzz FUZZTIME=5m` for longer
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
- A second, more structural bug in the same harness: `ReadFrame`
  originally bounded its wait with `(*os.File).SetReadDeadline`. On
  a `/dev/ptmx`-derived master fd in this environment, a deadline is
  accepted without error but does not actually unblock a pending
  `Read` - confirmed by watching a bare `Read` block 5+ seconds past
  a 500ms deadline in isolation. A test that expected zero bytes (to
  confirm a rejected command sends nothing) hung a `go test` run past
  its own `-timeout` because of this. Fixed by moving the read into
  its own goroutine and enforcing the timeout with `select`/
  `time.After` instead (`test/fakemcu/fakemcu.go`'s `readExactly`,
  unit-tested directly in `fakemcu_test.go` including a case that
  measures the elapsed time on a deliberate non-arrival, not just
  that *an* error eventually comes back). This matters because every
  pty-based test in this project depends on this timeout actually
  working if a real regression ever sends fewer bytes than expected -
  before this fix, that would have hung instead of failing. `make
  test`/`make test-race` also now pass an explicit `-timeout` as a
  second line of defense.
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
- A handful of frame-corruption bugs (toBCD/`EX4DateTimeFrame`,
  `shutdownFamily`, `EX4DisplayFrame`'s LEN byte - see git history)
  all came from the same root cause: a value landing directly in a
  wire frame without being checked against the frame's own FA/FB
  markers. `make fuzz` runs eight native Go fuzz tests, each clean
  for millions of executions:
  - `FuzzParse_NeverPanicsAndRoundTrips` (`pkg/board/board_test.go`)
    fuzzes `board.Parse` itself - the function that takes
    `-b/--board`'s value completely unvalidated, and the one every
    other Fuzz* target in this project is several layers downstream
    of. Checks that a successful parse is never `Unknown` and that
    its own `String()` re-parses to the identical board - the exact
    invariant `cmd/wd-control-tool/main.go`'s `setBoard` relies on
    when it compares two `-b/--board` occurrences by their resolved
    `board.Board` rather than their raw strings (see that fix).
  - `FuzzEX4DateTimeFrame_NeverEmitsMarkerBytesInPayload`
    (`pkg/mcu/legacy_test.go`) formalizes the frame-marker-collision
    property fixed by hand for specific boundary cases.
  - `FuzzEX4DisplayFrame_NeverEmitsMarkerBytesInPayload`
    (`pkg/mcu/legacy_test.go`) checks the same property for
    `EX4DisplayFrame`'s free-form text payload - and found a real,
    previously-unguarded instance on its first real run: a 250 or
    251-byte text makes the LEN byte itself (`byte(len(text))`) equal
    `0xFA`/`0xFB`, even with no colliding byte anywhere in the text.
    `EX4DisplayFrame` now rejects both that and any literal
    `0xFA`/`0xFB` byte inside the text itself.
  - `FuzzParseArgs_NeverPanics` (`cmd/wd-control-tool/main_test.go`)
    checks the hand-rolled argv parser's own bounds-checked
    indexing/slicing never panics on malformed input.
  - `FuzzRun_NeverPanics` (`cmd/wd-control-tool/main_test.go`) goes a
    layer deeper than FuzzParseArgs: it calls `run()` itself (with
    `-n` always forced, so a randomly-valid board+action combination
    can never reach real hardware I/O), exercising
    `buildLEDAction`/`buildFanAction`/`buildDisplayAction`/
    `buildRTCAction` and the `parseZone`/`parseColor`/`parseState`/
    `strconv.Atoi` calls they make - none of which any other fuzz
    target touched.
  - `FuzzPlan_NeverPanicsAndOnlyWritesKnownRegisters`
    (`pkg/led/led_test.go`) and
    `FuzzEx2FanPlan_NeverPanicsAndOnlyWritesKnownRegisters`
    (`pkg/fan/ex2_test.go`) check that `Zone`/`Color`/`State`/level
    values outside their declared constants (legal in Go - these are
    plain int-based types) never panic and never produce a register
    write to an address outside the small known set.
  - `FuzzEX2ULEDFrame_NeverPanics` (`pkg/mcu/ex2u_test.go`) throws
    arbitrary strings (long, unicode, embedded NUL/control bytes) at
    the one function in this codebase that takes free-form,
    CLI-user-supplied text without a length check of its own
    (EX2U's status LED only ever sends a fixed 7-byte frame
    regardless of the input string).
- End-to-end tests (`cmd/wd-control-tool/e2e_test.go`) build the real
  binary once and exec it for a few cases, closing the one remaining
  gap everything else leaves: `main()`'s own `os.Exit(run(...))` and
  the real process boundary (actual argv, actual process exit code).

Run `make test-race` after touching any of the above - the pty tests
use goroutines (the command runs concurrently with the test reading
the "wire"), so a race there would be a real concurrency bug, not a
test artifact.

Two logical (not memory-access) concurrency bugs were found this way
- `go test -race` alone wouldn't have caught either, since each
individual operation involved was still correctly mutex-protected in
isolation:
- `mcu.Port.sendBytes` held its lock per byte, not per frame, so two
  goroutines sending frames on the same Port could interleave their
  bytes on the wire. Fixed by holding the lock for the whole frame
  (`pkg/mcu/serial_test.go`'s
  `TestPort_SendBytes_ConcurrentFramesNeverInterleave`).
- `regio.Mem.SetClearBits` called the exported `ReadReg32` then
  `WriteReg32`, each independently taking and releasing the lock, so
  two concurrent `SetClearBits` calls on the same address could both
  read the same starting value before either wrote back - a classic
  read-modify-write lost update. Confirmed reliably reproducible (not
  just theoretical): `pkg/regio/regio_test.go`'s
  `TestMem_SetClearBits_ConcurrentDistinctBitsAllSurvive` (32
  goroutines each setting one distinct bit, no artificial delay)
  failed every single run against the old code. Fixed by holding the
  lock across both the read and the write.

A related test-quality bug, not a product bug: `regio.Mem`'s own
`TestMem_ConcurrentAccessIsRaceFree` claimed (in its doc comment) to
call `Close()` concurrently with the other 50 goroutines, but the
code actually called it strictly after `wg.Wait()` - with nothing
left running, `Close()` never raced against anything, so this test
could never have caught a regression in `Close()`'s own locking at
all. Fixed by launching `Close()` as its own goroutine started
alongside the others instead of afterward; added the same genuinely-
concurrent pattern for `mcu.Port.Close()`
(`TestPort_Close_ConcurrentWithSendBytesIsRaceFree`), which had no
such test before at all.

No automated test opens a real `/dev/mem` (only the sparse-file
stand-in) or sends traffic over a real UART to actual hardware - by
design, this suite proves the tool's own logic (frame bytes, register
math, CLI parsing/validation/exit codes) is correct, not that the
real MCU or SoC behaves as SPEC.md describes. That still needs real
hardware (see the project's "на реальном железе посмотим потом" note
- real-hardware verification is a separate, later step).

This limit is not hypothetical: real EX2U hardware testing found
exactly the gap this paragraph warns about. `-b ex2u -f 50` sent the
documented-correct 7-byte frame with no I/O error (every PTY-based
test agreed it should), but the fan never responded - `pkg/mcu/
ex2u.go` was sleeping 100ms between bytes based on an assumption
("the real hardware's own margin") that had never actually been
strace-confirmed, 200x slower than the real, working `mcu_ctl`'s own
~0.5ms-apart writes. The Welltrend MCU's own frame-reception state
machine most likely timed out between bytes and never saw a complete
frame - a failure mode no PTY (which has no such timeout at all)
could ever surface. Fixed by removing the deliberate delay entirely,
confirmed via a live `strace -tt` capture of the real tool (see
SPEC.md's "EX2U protocol"); see `TestEX2U_FanPercent_HasNoDeliberateInterByteDelay`
for the regression test this earned.

That pacing fix, while real, did not actually fix the hardware
symptom - a second real-hardware test after deploying it (binary
hash confirmed identical) still showed no response, same debug
output as before. The actual root cause, found the same way (a
direct `strace -x` comparison of this tool's own `TCSETS` call
against the vendor `mcu_ctl`'s): `pkg/mcu/serial.go`'s `Open` set
`Ispeed`/`Ospeed` on the termios struct but never OR'd the baud
constant into `Cflag` itself. `TCSETS` (the ioctl this tool uses,
matching the vendor tool - as opposed to `TCSETS2`) operates on the
kernel's plain `struct termios`, whose real C layout has no
`ispeed`/`ospeed` fields at all; the baud rate lives entirely in
`c_cflag`'s `CBAUD` bits. `unix.Termios`'s Go-side `Ispeed`/`Ospeed`
fields are silently ignored by a `TCSETS` ioctl, so this tool's
ports were all configured at the kernel's default `B0` (0 baud)
regardless of the requested baud - bytes went out at the wrong bit
rate, so the MCU's UART never saw a valid frame. The strace made
this directly visible: this tool's own `TCSETS` showed
`c_cflag=B0|CS8|CREAD|CLOCAL`; the vendor's showed
`c_cflag=B19200|CS8|CREAD|CLOCAL`. No PTY-based test could ever
have caught this (a pty has no bit rate to get wrong - every PTY
test in this suite passed identically before and after both the bug
and the fix). Fixed by OR'ing the baud constant into `Cflag`;
regression-tested by reading the configured termios back with
`TCGETS` and checking `Cflag&CBAUD` directly against the requested
baud (`pkg/mcu/open_test.go`'s `TestOpen_SetsRealBaudRateInCflag`,
confirmed to fail against the pre-fix code before being committed).

## What this does NOT cover

This repository builds only the `wd-control-tool` binary itself.
Packaging it into a board's rescue `uRamdisk` (busybox rootfs, init
script, cpio/gzip/mkimage) is a separate project and out of scope
here.

## Author

Symo <symops@gmail.com>
