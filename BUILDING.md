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

Current coverage is golden-byte unit tests for the frame-encoding
functions (`pkg/mcu/*_test.go`), checked against the worked examples
in `SPEC.md`. A no-hardware autotest harness (fake MCU over a PTY
pair, fake `/dev/mem`-backed register file) is planned but not yet
built - see the project's task tracker, not this file, for that
status.

## What this does NOT cover

This repository builds only the `wd-control-tool` binary itself.
Packaging it into a board's rescue `uRamdisk` (busybox rootfs, init
script, cpio/gzip/mkimage) is a separate project and out of scope
here.

## Author

Symo <symops@gmail.com>
