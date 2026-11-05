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

Coverage, all without real hardware and (`pkg/regio`'s
`TestOpen_OpensRealDevMem` aside - it opens and immediately closes
the real `/dev/mem` if root, skips if not, and never touches a
register either way) without root:

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

No automated test does real register I/O against `/dev/mem` (only the
sparse-file stand-in) or sends traffic over a real UART to actual
hardware - by design, this suite proves the tool's own logic (frame
bytes, register math, CLI parsing/validation/exit codes) is correct,
not that the real MCU or SoC behaves as SPEC.md describes. That still
needs real hardware (see the project's "на реальном железе посмотим
потом" note - real-hardware verification is a separate, later step).
(`pkg/regio`'s own `TestOpen_OpensRealDevMem` is a narrow exception:
it opens and immediately closes the real `/dev/mem`, confirming only
that `Open()`'s `os.OpenFile` wrapper itself works - it never calls
`ReadReg32`/`WriteReg32`/`SetClearBits`, so it never touches a single
physical register, and skips rather than fails where `/dev/mem` isn't
openable.)

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

A second, unrelated real-hardware gap found the same way, on EX2
this time: `led power blue off` (and every other `led power ...`
case) had no physical effect at all, even though the register writes
this tool computed exactly matched SPEC.md's documented `/dev/mem`
GPIO scheme. A live strace of the real vendor `led` tool showed why:
it opens **no `/dev/mem` at all** for the power LED - only
`/dev/ttyS1`, via the legacy MCU protocol's `NewPWRLed` command
(previously one of SPEC.md's own "anomalous, unexplained" table
rows). The documented GPIO mechanism had never actually been
hardware-confirmed for the power LED specifically - only statically,
via the same decompile that turned out to be right for HD-bay (a
second strace, of `led hd0/hd1 ... on/off`, DID show 4 `/dev/mem`
opens per command, confirming that half of the old claim) but wrong
for power. Fixed by adding `mcu.EX2PowerLEDFrames`/`(*EX2).PowerLED`
(the exact byte encoding decoded from 4 captured real-hardware
frames - blue/red x on/off) and routing EX2's `-l power ...` through
it instead of `pkg/led`/`pkg/regio` (`cmd/wd-control-tool/main.go`'s
`buildEX2PowerLEDAction`); EX2's hd0/hd1 and EX4's power LED are
untouched, the latter because no EX4 hardware was available to
re-verify it and changing it without evidence would just be a guess
in the other direction. See `pkg/mcu/ex2_test.go`'s
`TestEX2PowerLEDFrames` (golden-byte, from the real capture) and
`cmd/wd-control-tool/main_test.go`'s
`TestRun_RealRun_EX2PowerLed_SendsCorrectFramesForEachCase`.

Two more real-hardware findings, same EX2 fan subsystem: first, "EX2
level 2 spins faster than level 3" - `pkg/fan/ex2.go`'s level-to-
bitmask order had been *inferred* from the real binary's jump-table
case order (never independently verified per level, flagged as such
in SPEC.md), and that inferred order was wrong. Fixed by setting each
of this tool's own levels 1-6 on real hardware and reading the real
RPM back after each one (via the vendor `fan_control -g`, since this
tool couldn't read RPM yet at that point) - the real order turned out
to be a near-exact ~1000rpm-per-step progression once sorted by
measured RPM, not the jump table's case order.

That RPM read-back led to the second finding: `fan_control -g`'s
reply decodes cleanly. A live strace (with `read` traced, not just
`write` - the first attempt only traced `write` and missed the
reply entirely) captured `FA 03 09 00 00 3A FB` alongside `fan_control`
printing "fan rpm = 5172"; `0x3A` = 58, and `300000/58` truncates to
exactly 5172. The same formula round-trips exactly through all 6 RPM
values from the level-reordering investigation above (no raw byte
captured for those, but exact agreement across 6 independent
measurements is strong corroboration on its own). Added
`mcu.EX2DecodeFanRPM`/`(*EX2).FanStatusReply`, exposed as
`wd-control-tool -b ex2 -f rpm` (`-f`'s argument doubles as a command,
not just a level - see `cmd/wd-control-tool/main.go`'s
`buildEX2FanRPMAction`). See `pkg/mcu/ex2_test.go`'s
`TestEX2DecodeFanRPM_RealCapturedReply` and
`TestEX2DecodeFanRPM_CrossValidatedAgainstMeasuredLevels`.

A third EX2 LED finding, back on the power LED's MCU path this time:
`led power red on/off` (and switching between colors generally) was
intermittently unreliable on real hardware - sometimes needing more
than one attempt, a failed attempt typically going dark rather than
landing on either color. `PowerLED` sent its two `NewPWRLed` frames
back-to-back with no pause at all. A live strace of one successful
real `led power red on` showed the vendor itself pausing ~400ms
between the two frames (confirmed via its own write() timestamps); a
separate, failed attempt showed it instead retrying the first frame
7 times over ~2.7s before being killed - this protocol has no
checksum or acknowledgement, so nothing but timing/retries enforces
delivery. Fixed by adding the same ~400ms pause
(`pkg/mcu/ex2.go`'s `ex2PowerLEDInterFrameDelay`) - not a retry loop,
since no full successful multi-attempt sequence was ever captured to
copy exactly. See `pkg/mcu/ex2_test.go`'s
`TestEX2_PowerLED_PausesBetweenFrames` (confirms the pause actually
elapses, the opposite regression from the one this fixes).

A fourth EX2 finding, HD-bay this time (`led hd0/hd1 ...`): took TWO
rounds of wrong guesses to fix properly, and is the clearest example
in this whole project of why guessing a hardware address/bit from a
pattern match - even a plausible one - is not the same as confirming
it. Round 1: a real-hardware report showed neither color on hd0/hd1
had any effect; a live strace of the real, working `led hd0 red on`
showed its `mmap2` mapped page `0xF1018000`, not the `0xF1010000`
this tool used - the same page `pkg/fan/ex2.go`'s own confirmed
registers live in, strongly suggesting a transcription slip. That
fix kept the OLD in-page offset and bit formula on the NEW page -
plausible, and still wrong: round 2's real-hardware retest still had
no effect. Rather than guess a third time, a full scan of every word
in the `0xF1018000` page, before and after each real on/off
transition for both bays and both colors, settled it directly: the
two registers this tool thought were a Data-Out/Blink-Enable pair
(like Power's) actually change by the IDENTICAL bit together, every
time; red and blue use entirely different, non-overlapping bits per
bay; and the bit numbers (hd0 red=21/blue=25, hd1 red=20/blue=23)
follow no formula at all - "bit 13+N" (a real match for the
coincidentally-unrelated, always-constant `0xF1018100`) was simply
wrong. Fixed by replacing the formula with a confirmed lookup table
(`pkg/led/led.go`'s `hdBayBits`) and writing both registers
identically; "blinking" and hd2/hd3 (EX4-only, untested) now report
an explicit "unconfirmed" error instead of reusing a formula that had
just been disproven for the cases that WERE tested. See
`pkg/led/led_test.go`'s `TestSet_HDBayLED` (golden-byte, from the
real capture) and `TestPlan_HD2HD3Unconfirmed`.

A fifth EX2 finding, RTC this time: `RDateAndTime`/`WDateAndTime`
had been left unimplemented for EX2 entirely - the table's header
bytes were known but nothing had decompiled or captured the
write-side payload layout, and the reply format was the single
biggest open item in the whole spec. Two live `strace -x` captures
(`rtc -w` while the host's own system time was known to the second,
`rtc -r` while the vendor tool's own printed RTC time was known)
settled both at once: `WDateAndTime` is byte-for-byte the same
13-byte shape as EX4's (`FA b4 b5 b6 b7 <7 BCD fields> FB`), just
EX2's own header - with one correction to this spec's own table dump
along the way (the extracted b7=`01` for this row isn't what's
actually transmitted; the real wire byte is `07`, confirmed by the
capture matching the known time field-by-field). `RDateAndTime`'s
reply turned out to reuse that exact same 13-byte shape too - the
request's own header echoed back, followed by the 7 BCD fields -
rather than the request's own smaller frame, which nothing before
this capture could have predicted. Added `mcu.EX2DateTimeFrame`/
`EX2DecodeDateAndTime`/`(*EX2).SetDateAndTime`/`DateAndTimeReply` and
`rtc.EX2Writer`/`EX2Reader`/`SetSystemTime`, exposed as
`wd-control-tool -b ex2 -r w|r|s` (matching the real vendor `rtc`
tool's own three letters exactly, not the old full-word "write" this
project used before EX2's write was confirmed). `findFrame`
(`pkg/mcu/serial.go`) was generalized from a hardcoded 7-byte frame
to a variable header/length, since this reply is 13 bytes, not 7 -
see `pkg/mcu/ex2_test.go`'s `TestEX2DateTimeFrame_RealCapturedExample`
and `TestEX2DecodeDateAndTime_RealCapturedReply`. `s`'s second half
(actually setting the host's system clock, `rtc.SetSystemTime` via
`unix.Settimeofday`) is a real, hardware/OS-affecting side effect -
deliberately NOT exercised by this project's own automated tests,
the same reasoning as `pkg/regio.Open`'s real register writes: no
test run should change the clock of whatever machine runs it.

A real-hardware test of `-r r` right after this all shipped found a
sixth bug, in `recvBytes` itself (`pkg/mcu/serial.go`), not specific
to RTC: a real `RDateAndTime` reply (13 bytes) came back truncated to
8 bytes in a user's own `-D` capture (`reply=FA 01 08 01 01 47 12
14`, no trailing `FB`) - `EX2DecodeDateAndTime` correctly refused to
decode the genuinely incomplete frame rather than guess, but the real
bug was one layer down: `Open` configures `VMIN=1`/`VTIME=0`, so a
single `Read()` call returns as soon as just ONE byte is available -
it does not wait for a longer reply to finish arriving. This had
been silently "working" for every 7-byte reply this project had
decoded so far (temp, fan RPM) only because Go's own scheduling
latency happened to be slower than the inter-byte gap often enough
that several bytes were usually already queued by the time the
goroutine's `Read()` fired - not a guarantee, just a lucky margin
that a longer 13-byte reply exposed. Fixed by having `recvBytes` loop
(`readOnce`, factored out) across multiple `Read()` calls, applying a
short `interByteQuietWindow` (50ms - a heuristic margin, not a
captured value, chosen generous against the sub-millisecond gaps
this project HAS captured) between successive bytes rather than
settling for whatever the first `Read()` returned. See
`pkg/mcu/serial_test.go`'s
`TestPort_RecvBytes_AssemblesReplyArrivingOverMultipleReads` (writes
a real capture byte-at-a-time with a 1ms gap - confirmed to fail
against the pre-fix code, returning just the first byte, before this
fix) and `TestPort_RecvBytes_ReturnsAfterQuietWindowNotFullTimeout`
(confirms the fix doesn't make every reply as slow as a genuine
"nothing ever arrives" timeout).

After that fix shipped, chaining six `-r r` actions in one command
(zero think-time between them) surfaced a seventh issue: only the
first request got a clean reply - every one after it came back
corrupted/stale (fragments with no fresh header at all, several of
them byte-for-byte identical to each other), while a single isolated
`-r r` always worked. First response: a fixed pre-request pause
(`ex2RTCRequestSettleDelay`, 300ms, same shape as
`ex2PowerLEDInterFrameDelay`) - at the user's own request, rather
than another capture-and-diagnose round. It had **zero measurable
effect**: a repeat of the exact same chained-six-reads test produced
byte-for-byte identical garbled output before and after adding it.
That result itself was the real clue - a genuine MCU settle-time fix
would have shifted *something*, so the cause had to be on this
tool's own side, not the MCU's.

It was: `Port.recvBytes` (`pkg/mcu/serial.go`) spawned a fresh
goroutine calling `Read()` for every attempt at assembling a
multi-byte reply, including the short trailing "is there more"
probes added for the EARLIER truncated-reply fix (see above). When
such a probe's own short timeout elapsed with nothing new, its
goroutine was left behind still blocked on `Read()` - harmless in
isolation (this tool exits right after one action), but retrying a
request on the SAME `Port` (exactly what the EX2 RTC retry logic in
`cmd/wd-control-tool/main.go`'s `buildRTCReadAction` does, and what a
chain of separate actions sharing one `--serial` path does too, each
opening a fresh `Port` on the same physical device) could leave TWO
goroutines blocked on `Read()` at once - the kernel delivered the
next reply to WHICHEVER one it felt like, and that was reliably the
abandoned one, not the new call actually waiting for it. Reproduced
deterministically with a fake MCU
(`TestPort_RecvBytes_SecondCallOnSamePortSeesItsOwnReply`), not just
inferred from the real-hardware symptom.

The first fix attempt for THIS bug - one single persistent reader
goroutine per `Port` instead of one per read attempt, with `Close`
waiting for it to actually exit before returning - closed that race
but introduced a worse one: running the SAME test against a `Port`
with real termios settings applied (`CLOCAL`/`CREAD`/raw via
`TCSETS`, not a bare default pty) showed `Close` does **not**
reliably unblock a goroutine already parked in `Read()` on it -
confirmed directly (a small standalone reproduction: close from one
goroutine while another is blocked in `Read()` on the exact same
fd), not assumed. `Close` waiting for that goroutine would have hung
the whole CLI the first time two actions shared one `--serial` path
(reproduced as an actual test timeout, not a theory).

The fix that stuck: `Open` now configures `VMIN=0`/`VTIME=1` (not
`VMIN=1`/`VTIME=0`) - with `VMIN=0`, every `Read()` call is bounded
by the kernel itself (returns after ~100ms with nothing, or
immediately with whatever's available), so `recvBytes` can just loop
calling `Read()` directly, synchronously, with no goroutine ever
left blocked on anything and nothing for `Close` to wait for.
`test/fakemcu`'s own `setRaw` was updated to the same `VMIN`/`VTIME`
values, since a fake pty under the OLD config wouldn't exercise the
same blocking behavior real hardware now does. See
`pkg/mcu/serial.go`'s `readPollInterval` doc comment for the full
three-attempts history in one place.

`cmd/wd-control-tool/main.go`'s `buildRTCReadAction` still retries a
decode failure a few times (`ex2RTCMaxAttempts`) as defense in depth
- this protocol has no checksum or acknowledgement at all, so a
genuinely corrupted reply (for whatever reason) is still possible in
principle, even with the actual bug fixed.

An eighth finding, once reads/writes were both reliably getting
through: the real `-w`/`-r` round-trip looked correct in isolation
(write a value, read it back, same value) but was never checked
against an INDEPENDENT clock. Running `date` and the vendor `ex2.sh
"rtc -w"` back to back in one session on real EX2 hardware showed
`date` printing the host's local (`+03`) time while the vendor
tool's own `rtc: system time: ...` line printed exactly 3 hours
earlier - the vendor applies NO timezone adjustment at all; the RTC
stores UTC. `wd-control-tool`'s `EX2Writer`/`EX4Writer.Set`
(`pkg/rtc/rtc.go`) were extracting fields directly from whatever zone
their input `time.Time` happened to carry - on a host where
`time.Local` isn't UTC (this one), every write was off by the host's
own timezone offset, and `EX2Reader.Read`'s result was tagged
`time.Local` on top of UTC-valued fields, compounding the same
mistake on read-back - meaning `-s` (sync) would have set the host's
system clock to the WRONG absolute moment, silently, every time.
Every existing test for this had constructed its own example times
already tagged `time.UTC` directly, so none of them could have
caught this - fixed by converting to UTC in `Set` and tagging
`Read`'s result `time.UTC`, with a new pair of regression tests
(`TestEX4Writer_Set_ConvertsNonUTCInputToUTC`/
`TestEX2Writer_Set_ConvertsNonUTCInputToUTC`) that construct their
own example time in a non-UTC zone specifically so this class of bug
can't hide behind an all-UTC test suite again - confirmed to fail
against the pre-fix code (reverted it, ran them, saw the wrong hour
byte, restored the fix) before trusting them.

A ninth, much smaller finding: a real `-r w` run printed NOTHING on
success - `buildRTCWriteAction`'s `apply` just returned
`EX2Writer`/`EX4Writer.Set`'s own error (or nil) with no output of
its own, unlike every other rtc subcommand ("r"/"s" both print "RTC
time = ..."). The vendor `rtc -w` itself prints "system time: ...".
Fixed by printing the same wording on success, using the exact UTC
value already computed for the `-D` frame preview (not a second,
independently-computed `time.Now()`, which would drift a few
milliseconds from what was actually sent).

A tenth finding, found by chaining `rtc r`+`rtc w`+`rtc s` (`-D`)
many times in a row with a few seconds between attempts (to mimic
how the bug was first noticed - manual, unhurried typing, not a
tight scripted loop): a `RDateAndTime` read immediately following a
`WDateAndTime` write intermittently came back with SOME fields
correct and others wrong. One captured example (write
`sec=48,min=49,hour=15,dow=01,dom=02,mon=11,year=26`, the very next
read): `sec=48,min=49,hour=15` (identical, correctly fresh) but
`dow=02,dom=11,mon=10,year=01` (all wrong - not the just-written
values, not random noise either: a well-formed, decodable frame,
just semantically stale/mixed). The SAME wrong value then reappeared
on the NEXT, completely independent process invocation's own read -
not until the FOLLOWING write did a read come back correct again.
That "stays wrong until the next write, right frame shape" signature
points at a race inside the MCU's own firmware between its
`WDateAndTime` and `RDateAndTime` handlers specifically when a read
lands too soon after a write - a different trigger from the
earlier-tested (and disproven) "settle delay between consecutive
READS" idea, never isolated on its own before. A second mitigation
attempt, `ex2RTCPostWriteSettleDelay` (300ms, added after a
successful write inside `EX2Writer`/`EX4Writer.Set` itself), was
retested with the EXACT same repeated-chain reproduction and also
had **zero measurable effect** - the bug recurred (iteration 6 of
10, same "right frame, specific fields wrong" shape, this time only
`year`). Removed rather than left in place doing nothing: this
project does not keep a "fix" around once real-hardware testing
disproves it, matching how the earlier (also-disproven)
`ex2RTCRequestSettleDelay` was removed, not kept as dead weight.
Next step: compare this same repeated-chain reproduction against the
VENDOR's own `rtc -r`/`rtc -w`/`rtc -s` (necessarily as separate
process invocations, since the vendor tool has no chaining concept
at all) - if the vendor's own tool shows the identical "right frame,
wrong field(s), sticky until next write" pattern, that confirms this
is purely an MCU-firmware race with no fix available on this
project's side at all, and also answers whether crossing a real
process boundary (not just elapsed time) matters, since the vendor
can never NOT cross one between actions.

That comparison ran next: the vendor `rtc` binary run DIRECTLY (not
via its own `ex2.sh` wrapper), 50 iterations of `r`+`w`+`s` with zero
sleep between the three and a real process boundary between each
(exactly this tool's own worst case) - **100% clean, zero
corruption**, every single run. This disproved "purely an MCU race
nothing can be done about" - if it were, the vendor would hit it too
at the same rate, and it never did. Meanwhile, `wd-control-tool` run
the identical way (three separate invocations, 1s between iterations,
50 iterations) still failed ~5-10 times per 50, ruling out "crossing
a process boundary" as the trigger on its own (the vendor crosses one
every time and stays clean) - something about what THIS tool does
differently from the vendor's own tool was still the open question.

Two more things were tried and ALSO disproven before the real cause
was found, both still chasing "the MCU just needs more settle time":
inter-byte write pacing raised from the vendor-matching 10us up to
10ms/byte (1000x, via a temporary `WD_LEGACY_BYTE_DELAY_US`
diagnostic env var, since removed) had zero effect on the failure
rate even at that extreme; and wrapping the whole reproduction in
`strace` (to capture the exact wire bytes around a real failure
without guessing) made the corruption **stop reproducing entirely**
across 3 full 50-iteration runs - not evidence the bug was fixed, but
evidence `strace`'s own per-syscall tracing overhead was itself
perturbing the timing enough to mask the very thing being measured,
so the capture had to happen WITHOUT `strace`.

The fix for that: `WD_MCU_TRACE`, a lightweight in-process trace (no
ptrace, no extra syscalls beyond a stderr write) gated behind an env
var, added directly to `pkg/mcu/serial.go` (`sendBytes`,
`recvBytes`, `flushInput` - the last one also now logs how many
bytes `FIONREAD` reports pending right before every flush, to check
for stale buffered data directly rather than inferring it). Run
without `strace`, this finally captured the actual failure byte-for-
byte. The decoded corrupted frame
(`FA 01 08 01 01 20 29 01 02 11 10 01 FB`) against what had just been
written (`sec=20,min=29,hour=20,dow=01,dom=02,mon=11,year=26`) showed
`sec`/`min` exactly right while `hour`/`dow`/`dom`/`mon`/`year` were a
DIFFERENT, internally well-formed stale snapshot
(`hour=01,dow=02,dom=11,mon=10,year=01`) - and the trace around both
the write and the read immediately before it showed `flush: 0
byte(s) pending` and a single clean 13-byte frame each time: this
tool's own transport was doing nothing wrong. The SAME wrong
`hour`/`dow`/`dom`/`mon`/`year` snapshot reappeared, byte-for-byte
identical, on the NEXT read 1.3s later (`sec` ticking forward
normally in between) from a completely separate process invocation -
and the write right after that cleared it, the following read coming
back correct. This is conclusive: EX2's MCU firmware reads `sec`/
`min` live off the RTC chip on every `RDateAndTime` request, but
caches/freezes `hour`/`dow`/`dom`/`mon`/`year` separately in a way
that sometimes doesn't refresh, and only a `WDateAndTime` write
resets that cache - a genuine MCU-firmware bug, not a transport
timing issue (consistent with why neither pacing attempt helped: the
MCU was never waiting on bytes, it was serving stale cached fields no
matter how the request arrived).

Since a write is the one thing confirmed to clear the freeze,
`(*EX2).SetDateAndTime` (`pkg/mcu/ex2.go`) now reads straight back
what it just wrote and re-sends the write if the readback doesn't
match (`ex2RTCVerifyAttempts`, 3 tries) - this works specifically
because, unlike any general read, the write call KNOWS the exact
value it just sent and can check against it exactly, rather than
guessing at a "plausible" date. That is the key difference from the
implausible-year sanity-check/retry idea rejected earlier in this
investigation (see above): that one would have mistaken a genuinely
dead RTC battery's old-but-real value for corruption; this one only
ever compares against a value this same call just wrote, so it can
never misfire on a standalone read. The flip side, as understood at
the time: a standalone read with no preceding write in the same
process (a cold `rtc -s` at boot, in particular) has no such ground
truth available and remains unprotected.

**That "MCU firmware cache" diagnosis turned out to be wrong** - it
explained the symptom (sec/min live, everything else frozen) but not
the actual mechanism, and the verify-retry fix above, while real
defense in depth, was not the actual fix. Real-hardware testing kept
finding the corruption at roughly the same rate (~5-10 per 50)
regardless: inter-byte write pacing raised to 10ms/byte (1000x the
vendor's own 10us) had zero effect; a delay inserted specifically
between `SetDateAndTime`'s own verify-retry attempts, tried at 300ms
and then deliberately pushed to 5000ms, ALSO had zero effect (the
failing attempt's reply was consistently nothing but the unsolicited
noise frame, for the full 5 seconds, every time) - ruling out
"insufficient settle time" at any magnitude as the mechanism, since
5 seconds is not a subtle amount of slack. A live-monitoring check
(polling `/proc` via `fuser`, confirmed fast enough on this hardware
to actually catch this tool's own ~140ms open-to-close window after
an earlier `find`-based version proved too slow at ~350ms/scan to
catch anything at all) found no other process ever holding
`/dev/ttyS1` open during a failure, ruling out a competing daemon.

Comparing this tool's own `WD_MCU_TRACE` byte-level trace against
the real vendor daemons directly settled it. The vendor `rtc` CLI
does not touch `/dev/ttyS1` itself at all - it talks over a SysV
message queue to `up_send_daemon` (which writes requests) and a
second, independent `up_read_daemon` (continuously busy-polling
`/dev/ttyS1` one byte at a time, ~49% CPU, found via `ps` by name
after locating its PID through `/tmp/msg_info.txt`), with completion
signaled through yet another IPC path (an `xmldbc` config-database
round-trip clearing/setting a `/runtime/mcu/ack` key - unrelated to
the raw UART bytes, just the vendor's own handshake plumbing). The
vendor's own `/tmp/RtcRetryLog` showed this needs a timeout-retry on
nearly every single call, not rarely - "the vendor never has this
problem" was never actually true, it is just hidden well behind a
persistent daemon architecture and an internal retry loop whose
failures don't reach the CLI's own stdout. A 30-iteration stress run
with `strace` attached directly to `up_read_daemon` (filtered to
raw `read()`/`write()` on its UART fd, parsed byte-by-byte afterward
with a decoder careful about `strace -x`'s C-string escaping of
`"`/`\` - an early version of that parser silently dropped exactly
those two byte values and manufactured a false "frozen field"
artifact that evaporated once fixed) found the daemon's own 155
captured replies all correct, no corruption observed - a negative
result, not proof the vendor is immune, but no positive evidence of
the MCU-side cache theory either.

The one architectural difference nothing above addressed: the
vendor's daemons open `/dev/ttyS1` ONCE and keep it open for the
process's entire lifetime; this tool opens and closes it on every
single invocation. Deliberately not "fix this by adding a daemon"
(ruled out directly - this project is moving away from needing one,
not toward it) - instead, a live strace of the daemons' own startup
(killed and restarted them under `strace -f` to catch the `open()`+
`ioctl(TCSETS)` sequence a steady-state attach would miss) was
compared byte-for-byte against this tool's own `mcu.Open`: the
vendor's `open()` has no `O_SYNC` (this tool's did), and its
`c_iflag` is `IGNPAR` (this tool's was 0). Both were matched exactly
as a test - zero effect, same failure signature, same rate. At this
point the real cause was found by direct reasoning rather than more
comparison: the failing verify-read attempts always got back ONLY
the unsolicited noise frame and nothing else, within a trace already
showing every step completing cleanly (0 bytes pending at flush, a
write sent, a read request sent) - meaning the MCU was not sending
back a stale value at all, it was not responding to a proper command
because it may never have fully received one. `Port.sendBytes`
(`pkg/mcu/serial.go`) returns as soon as the last byte is handed to
the kernel's `write()` - at 115200 baud a 13-byte frame still takes
~1.1ms to actually finish shifting out over the wire, and `write()`
returning only means it was queued. `SetDateAndTime`'s own
verify-read calls `flushInput()` (`ioctl(TCIFLUSH)`) under 1ms after
that write call returns, specifically to discard anything stale
before sending the read request - and on this UART driver, an input
flush issued that soon after a write can also corrupt the tail of
that write's still-in-flight OUTPUT, which is not documented POSIX
behavior for `TCIFLUSH` but is what real-hardware testing confirmed.
**Fixed**: `sendBytes` now drains (`ioctl(TCSBRK, 0)` - the kernel's
own `tcdrain()` equivalent, waiting exactly as long as transmission
actually takes, not a fixed sleep, the same principled distinction
that made this a worth trying after several fixed delays had already
failed) before returning, so any caller's own `flushInput()` runs
only after the write it just sent has genuinely finished. Confirmed
on real hardware: multiple full 50-iteration stress runs with zero
corruption after this one-line fix, on a test that had reliably
reproduced the bug on every prior run. The write-then-verify-retry
loop from the (wrong) MCU-cache diagnosis is kept as defense in
depth - it is not wrong to have, just not the actual fix, and it now
essentially never needs to retry at all.

A twelfth finding: implementing EX4's `-r r`/`-r s` (previously
unimplemented - EX4's own `RDateAndTime` reply had never been
captured) surfaced a real, previously-undetected bug in EX4's own
write side. A live capture of `rtc -r` on real EX4 hardware decoded
the reply cleanly (`FA 01 00 03 07 57 52 20 03 04 11 26 FB`, year/
mon/dom/hour/min/sec all exactly matching the printed date) - but the
vendor's own print line said "Tue" for 2026-11-04, which is actually
a Wednesday (2026-11-01 is a Sunday, used elsewhere in this project's
own tests). The decoded `dow` byte was `3`, byte-for-byte identical
to what this tool had itself written moments earlier in the same
capture - ruling out a storage-side transform - so the only
explanation is that EX4's own firmware expects `dow` as
`1=Sunday..7=Saturday`, not EX2's (separately, hardware-confirmed)
`0=Sunday..6=Saturday`. `EX4Writer.Set` had been sending the plain
0-indexed value, with a doc comment claiming this was "confirmed
day-by-day for all 7 days" - that claim only ever checked the byte
the method itself wrote, never the vendor's own interpretation of it,
since EX4 had no read support to check against until now. Fixed
(`dow := int(t.Weekday()) + 1`); this tool's own printed weekday was
never actually wrong for EX4 either way, since `buildRTCReadAction`
computes it from `time.Date(...).Weekday()` off the real year/mon/dom
fields, not from the wire's own dow byte - only the RAW BYTE SENT TO
THE MCU was wrong, silently, until a read-back capture could finally
check it.

A thirteenth finding: EX4's HD-bay LEDs (`hd0`..`hd3`) turned out to
use a DIFFERENT mechanism from EX2's, not an identical one just moved
to a different board - a real-hardware report showed EX2's confirmed
hd-bay registers had no effect on EX4 at all, and a second report
found EX4's blue hd-bay LEDs toggle all four bays together while red
toggles each bay independently, something EX2's uniform per-bay-bit
model structurally cannot produce either way. A live strace (per
zone/color/state, 16 captures total) found two different `/dev/mem`
pages, one per color (`0xf1010000` for red - the same page as EX4's
power LED, just different registers within it; `0xe0010000` for blue,
identical across every bay), and decompiling the real `led` binary
confirmed exactly why: its blue handler (`FUN_0000ab20`) reads the
bay-index argument, validates it's 0..3, and then never uses it again
- it always writes the same literal value to the same single register
regardless of which bay was asked for. Implemented as
`pkg/led.EX4HDBayPlan`, dispatched separately from the EX2-only
`Plan`/`hdBayBits` path - see SPEC.md's "HD-bay".

A process-level lesson from the SAME investigation: the first attempt
at this strace capture put `strace` INSIDE the command string passed
to `ex2.sh` (i.e. `ex2.sh "strace ... led ..."`), which runs inside the
vendor's own `/tools` chroot - busybox only, no `strace` binary there
at all. Every one of those 16 "captures" silently logged `sh: strace:
not found` with a perfectly successful (0) exit status (from
`ex2.sh`'s own wrapper, which doesn't propagate the inner command's
failure), so nothing about the run looked wrong until a log file's
actual contents were checked instead of trusting the exit code. The
fix: wrap `ex2.sh` itself with `strace -f` from the HOST shell, not
inside the string handed to it - `-f` follows the fork/exec straight
through `ex2.sh`'s own `chroot()` call into `led`, which is how every
earlier successful capture in this project (fan RPM, RTC, power LED)
had always done it; this was the first time the mistake was actually
made, and only visible once a log's text was read rather than its exit
status.

A smaller, process-level lesson from the same work: `make strip`'s
real armv7/armv5 cross-compile caught a build failure (`EX4FanTachAddr`,
`0xf1010150`, overflowing a plain 32-bit `int`) that `go vet`/`go test`
never catch, since those always run on this project's amd64 dev host,
where `int` is 64 bits. An untyped Go constant passed to an
`interface{}`/`any` parameter (`fmt.Sprintf`, in that case) defaults
to plain `int`, not `int64` - any future register-address constant
above `0x7fffffff` needs an explicit `int64` type at its own
declaration, not just at its use site, or this surfaces again as a
cross-compile-only failure.

A fourteenth finding: adding EX4's LCD sleep mode (`-s`/`--sleep`)
needed no decompilation at all, unlike almost everything else in this
project - `LCDSleepMode` is an ordinary row in the already-extracted
82-row command table (`up_send_ctl`'s own `.data` section dump, see
SPEC.md), so the real `up_send_ctl LCDSleepMode 0`/`LCDSleepMode 1`
already knows how to build and send it correctly; a live strace of
those two real, working invocations captured the exact wire bytes
directly. The one real pitfall was semantic, not a protocol-decoding
one: the vendor's own usage text (`0: OFF, 1: ON`) is genuinely
ambiguous about WHAT is off/on, and a first real-hardware report of
value 1 -> screen lit / value 0 -> screen dark turned out to be
backwards on a careful retest (value 1 actually puts the screen to
sleep). Lesson: for a binary on/off value whose own semantics aren't
pinned down by a worked example, ask for one command's effect at a
time, not the pair taken together - the ambiguity disappears
immediately in a single clean observation, but can silently flip
between a quick, memory-driven recollection of "which one did I just
run" after both were tried close together.

A fifteenth finding: EX2's own "blinking" state, previously an
unconfirmed guess-avoidance error for both power and HD-bay, turned
out to actually work on real hardware via the vendor's own tool -
found only because the user tried it directly and reported back,
not from any capture this project had already taken. Power's MCU
frame decoded the same way its on/off frames always had (clear-
other/set-own, now with a THIRD flag bit, `0x20`, alongside the
existing `0x10` "on" flag) from a plain strace, no surprises there.
HD-bay's own confirmation needed a genuinely different technique:
`/dev/mem` writes aren't syscalls, so strace shows nothing for them
(same limitation this project already knew about) - but a plain
before/after/during READ of the two known registers (via a tiny
standalone helper reusing `pkg/regio` directly, cross-compiled and
copied to the board) caught something more interesting than a
static bit flip: `hdBayReg2`'s own bit, read twice in a row with NO
command run in between, had already changed value on its own - the
SoC's own hardware blink generator actively toggling the pin in real
time, confirmed by observation rather than inferred. `hdBayReg1`
stayed completely constant throughout, so the real write this
project's own code now performs is "set the bit in reg2 only," not
a guess extrapolated from on/off's "touch both registers
identically" pattern - the two states genuinely don't share a
mechanism. Only RED was reported as working by the user (hd0 and
hd1) - BLUE's own HD-bay blinking frame remains exactly as
unconfirmed as before and still errors, not assumed symmetric with
red just because the mechanism rhymes.

A sixteenth finding, and a correction to the fifteenth: the HD-bay
RED "confirmation" above was wrong. On real hardware, `wd-control-tool
-b ex2 -l hd0/hd1 red blinking` had no visible effect - the write
this project shipped (set `hdBayReg2`'s bit, leave `hdBayReg1` alone)
does not actually make the LED blink. Re-examining the original
capture: "read `hdBayReg2` twice in a row and see it oscillate" is
real evidence that the hardware blinks *when it's already blinking*,
but it is NOT evidence that *writing* `hdBayReg2` is what *starts*
blinking, and "`hdBayReg1` stayed constant throughout" only means it
didn't change DURING that capture - if the bay was already left in
some other state (e.g. still `on` from an earlier step in the same
session, with `hdBayReg1`'s bit already at 1) before the blinking
command ran, "constant" is consistent with `hdBayReg1` being
irrelevant AND with it being required-and-already-satisfied; the
capture couldn't distinguish those two. The real lesson: confirming
what controls a STATE TRANSITION needs a diff between a known state
(e.g. a confirmed `on`) and the claimed new state (`blinking`) across
every register that might matter - not two reads taken seconds apart
while already inside the state being investigated, however dramatic
the "it changed with nothing in between" moment looks. This project
reverted HD-bay blinking to erroring for both colors
(`errHDBayBlinkingUnconfirmed`) pending a capture built that way: dump
a wide register range (e.g. via `regdump range 0xSTART 0xEND`, an
extended version of the earlier single-address helper) right after a
confirmed `on`, dump it again right after switching to `blinking`,
and diff the two dumps directly - whatever bit(s) differ between
those two concrete states, and only those, are candidates for the
real blink-enable control.

## What this does NOT cover

This repository builds only the `wd-control-tool` binary itself.
Packaging it into a board's rescue `uRamdisk` (busybox rootfs, init
script, cpio/gzip/mkimage) is a separate project and out of scope
here.

## Author

Symo <symops@gmail.com>
