# Building wd-control-tool from a clean Debian 13

This describes building **wd-control-tool** (the self-contained
replacement for the proprietary cooler/RTC/LED/display control stack
on three WD My Cloud boards - EX2 Ultra, EX2 Gen2 and EX4 Gen2) from
scratch on a fresh Debian 13 ("trixie") machine, running its tests,
and using it on a board. See `README.md` for what the tool does and
`SPEC.md` for the full reverse-engineered hardware reference it
follows. See `HISTORY.md` for the project's own testing history -
the real bugs, false alarms and techniques that found each one - kept
out of this file to keep it a plain build/test/release guide.

If you only want to run it, you do not need any of this: download the
ready-made binary for your board from the
[latest release](https://github.com/symops/wd-control-tool/releases/latest).

## 1. Install the toolchain and build dependencies

```sh
sudo apt update
sudo apt install -y \
    git make golang-go \
    gcc-arm-linux-gnueabihf gcc-arm-linux-gnueabi
```

Go 1.22 or newer is required (`go.mod`); Debian 13's `golang-go`
satisfies it. Check with `go version`.

Two different cross C compilers are needed, one per target ABI - not
because this is a cgo project (it isn't: no `import "C"` anywhere),
but because `make build-ex2u`/`make build-ex4` use
`CGO_ENABLED=1` + `-linkmode=external` specifically to force a
*dynamically*-linked binary against the target's own glibc, matching
`verona-tool`'s own approach (see the `LDFLAGS` comment in the
`Makefile` for why `CGO_ENABLED=1` alone does not force this), and
`cgo` needs a working C cross-compiler on the host for that:

- `gcc-arm-linux-gnueabihf` (+ `-strip`, `-readelf`) for armv7
  hard-float - EX2 Ultra, and EX2 Gen2 once its uRamdisk is rebuilt.
- `gcc-arm-linux-gnueabi` (+ `-strip`, `-readelf`) for armv5
  soft-float - EX4 Gen2's current target, and EX2 Gen2's *current*
  uRamdisk (even though its real silicon is armv7 too).

## 2. Clone the repository

```sh
git clone https://github.com/symops/wd-control-tool
cd wd-control-tool
go mod tidy   # fetches golang.org/x/sys - the only non-stdlib dependency
```

There is no `.config` or any other generated input: the repository is
the whole source.

## 3. Build the ARM binaries

```sh
make build-ex2u   # -> wd-control-tool-armv7 (EX2 Ultra, future EX2 Gen2)
make build-ex4    # -> wd-control-tool-armv5 (EX4 Gen2, current EX2 Gen2)
```

Each runs, respectively:

```sh
CGO_ENABLED=1 CC=arm-linux-gnueabihf-gcc GOOS=linux GOARCH=arm GOARM=7 \
    go build -ldflags '-s -w -linkmode=external' -o wd-control-tool-armv7 ./cmd/wd-control-tool

CGO_ENABLED=1 CC=arm-linux-gnueabi-gcc GOOS=linux GOARCH=arm GOARM=5 \
    go build -ldflags '-s -w -linkmode=external' -o wd-control-tool-armv5 ./cmd/wd-control-tool
```

**`-linkmode=external` is explicit and required.** `CGO_ENABLED=1`
alone does not force dynamic linking when the package contains no
real cgo code: Go's linker still defaults to a fully static binary in
that case. The target (a board's rescue uRamdisk) ships a normal
glibc userspace, so a dynamic binary is what is wanted, and `make
verify-deps` (next step) proves it needs nothing the target lacks.

`make build-ex2` is an alias for `make build-ex2u`, kept for when
EX2 Gen2's own uRamdisk is rebuilt onto armv7 - until then it produces
the identical, armv5 `build-ex4` output EX2's current uRamdisk needs.

## 4. Verify the dependency set

```sh
make verify-deps
```

Builds both binaries, then checks each one's `NEEDED` entries
(`readelf -d`) and fails if either needs any shared library beyond
`libc.so.6` - the whole point of this tool is dropping the vendor
`.so` stack entirely. If a code change ever pulls in another shared
library, fix the dependency rather than ignoring the failure.

## 5. Strip

```sh
make strip
```

Runs `verify-deps` first, then `arm-linux-gnueabihf-strip`/
`arm-linux-gnueabi-strip` on the two binaries. **Always produce the
binaries you ship this way**, not with a bare `go build`: skipping
`verify-deps` risks shipping a binary that silently needs a library
the real board doesn't have.

Confirm the result is actually dynamically linked before shipping it
- this has silently regressed to a static binary before (see the
`LDFLAGS` comment in the `Makefile`):

```sh
file wd-control-tool-armv7
# expect: ... dynamically linked, interpreter /lib/ld-linux-armhf.so.3 ...
arm-linux-gnueabihf-readelf -d wd-control-tool-armv7 | grep NEEDED
# expect: only libc.so.6

file wd-control-tool-armv5
# expect: ... dynamically linked, interpreter /lib/ld-linux.so.3 ...
arm-linux-gnueabi-readelf -d wd-control-tool-armv5 | grep NEEDED
# expect: only libc.so.6
```

## 6. Host build and unit tests

```sh
make build-host   # -> wd-control-tool (native linux/amd64, for local iteration)
```

A plain `CGO_ENABLED=0` build for your own machine - useful for
exercising argument parsing and most of the test suite without
cross-compiling, but it cannot touch `/dev/mem` or a real serial port
meaningfully on a dev machine, so it is not a substitute for testing
on actual hardware.

```sh
make test        # go vet + go test ./...
make test-race   # go vet + go test -race ./... - run before every push, not just this
make fuzz        # actually fuzz (not just the seed corpus `test` runs) - 30s per target by default, `make fuzz FUZZTIME=5m` for longer
```

All of this runs without real hardware and (one narrow exception -
`pkg/regio`'s `TestOpen_OpensRealDevMem`, which opens and immediately
closes the real `/dev/mem` if root and skips if not, never touching
an actual register) without root. It exercises the tool's own logic -
frame bytes, register math, CLI parsing/validation/exit codes -
against a real Linux pty pair (`test/fakemcu`) and a sparse file
standing in for `/dev/mem` (`test/fakeregio`), plus golden-byte checks
against `SPEC.md`'s own worked examples and end-to-end CLI tests that
build and exec the real binary. It does not prove the real MCU or SoC
behaves as `SPEC.md` describes - that needs real hardware, which is
a separate, later step. `make fuzz`'s own worker count is capped at
25% of the machine's CPUs (`GOMAXPROCS`, see the `Makefile`'s
`FUZZCPU`) so a long fuzz run doesn't starve everything else on a
shared machine.

See `HISTORY.md` for what each of these layers has actually caught
over this project's life, including two real concurrency bugs no
amount of `-race` alone would have found, and several real-hardware
gaps no amount of software-only testing could have.

## 7. Run it

```sh
# Preview only - prints what would be done, touches nothing:
./wd-control-tool-armv7 -b ex2u -n -D -f 50

# Real hardware (needs root for /dev/mem-based actions; no confirmation prompt):
sudo ./wd-control-tool-armv5 -b ex4 -l power blue on
sudo ./wd-control-tool-armv7 -b ex2u -t
```

See `README.md`'s "Quick start" and "Command reference" for the full
set of actions (`-l`/`--led`, `-f`/`--fan`, `-r`/`--rtc`,
`-d`/`--display`, `-t`/`--temp`, `-s`/`--sleep`) and `SPEC.md` for the
protocol/register details behind each one.

Useful facts when working with it:

- **`-b`/`--board` is required** and fixed for the whole command
  (`ex2u`, `ex2` or `ex4`) - it may appear anywhere, but a second
  occurrence resolving to a *different* board is a usage error, not a
  way to switch boards partway through one command.
- **Several actions can be chained** in one command and run in the
  order given; on a real (non-dry-run) run, the first one that fails
  stops the rest.
- **`-n`/`--dry-run` and `-D`/`--debug`** may appear anywhere and
  apply to every action - always prove a command with `-n -D` first
  against hardware you can't easily recover if the real write is
  wrong.
- **Exit codes**: `0` success, `1` argument/usage error, `2` not
  supported on this board, `3` hardware I/O error. Full table in
  `README.md`.

Real (non-dry-run) actions touch real hardware the moment they run -
there is no prompt. Prove the exact command with `-n -D` first, and
double-check `-b`/`--board` before running anything against a real
board.

## 8. Make a release

The release assets are the two stripped binaries, named exactly as
`make strip` produces them (`wd-control-tool-armv7`,
`wd-control-tool-armv5`) - copy the matching one to a board and
`chmod +x` it.

```sh
git switch main && git pull --ff-only
make strip                                   # build + verify-deps + strip, both binaries
sha256sum wd-control-tool-armv7 wd-control-tool-armv5
gh release create vX.Y wd-control-tool-armv7 wd-control-tool-armv5 \
    --target main --title "vX.Y" --notes-file notes.md
```

Put the SHA-256 of each binary in the release notes. The version
printed by `wd-control-tool -v`/`--version` is the `version` constant
in `cmd/wd-control-tool/main.go`: bump it before tagging so the
binaries and the tag agree.

## What this does NOT cover

This repository builds only the `wd-control-tool` binary itself.
Packaging it into a board's rescue `uRamdisk` (busybox rootfs, init
script, cpio/gzip/mkimage) is a separate project and out of scope
here.
