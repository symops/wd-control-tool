# wd-control-tool

A self-contained replacement for the proprietary cooler/RTC/LED/
display control stack on three WD My Cloud boards - EX2 Ultra
(EX2U), EX2 Gen2 and EX4 Gen2. Replaces `mcu_ctl` (EX2U) and
`fan_control`/`rtc`/`led`/`up_send_ctl` plus their supporting daemons
(`xmldb`, `up_read_daemon`, `up_send_daemon`) and vendor `.so` stack
(EX2/EX4) with one tool that talks to the hardware directly: the
front-panel MCU over UART, and `/dev/mem`-mapped registers for LEDs
and EX2's fan.

Motivation: get rid of a pile of outdated proprietary libraries and
daemons just to blink an LED or spin a fan - no vendor `.so`, no XML
config daemon, no message-queue relay, no `/tmp` file polling.

See `SPEC.md` for the full, reverse-engineered hardware reference
this implementation follows - including what is still unconfirmed
or unimplemented (most notably: this tool cannot yet read anything
back from either board's MCU - every command is fire-and-forget; see
SPEC.md, "Request/response and acknowledgement").

## Download

Ready-made binaries are attached to the
[releases](https://github.com/symops/wd-control-tool/releases):
stripped, dynamically-linked ARM executables (glibc only), sized
comparably to [verona-tool](https://github.com/symops/verona-tool)'s
own releases. `armv7` for EX2U (and, once its uRamdisk is rebuilt,
EX2); EX4's target is still `armv5` soft-float pending confirmation
of its real silicon. Copy the matching binary to the board and
`chmod +x` it.

## Quick start

```sh
wd-control-tool -b ex2u -f 50                        # EX2U: 50% duty cycle
wd-control-tool -b ex2u -l breathing                 # EX2U: status LED breathing

wd-control-tool -b ex2 -f 3                           # EX2: ~6000rpm step (see SPEC.md - exact step order not independently confirmed)
wd-control-tool -b ex2 --led power blue on

wd-control-tool -b ex4 -f 2                            # EX4: Medium
wd-control-tool -b ex4 --led power blue on
wd-control-tool -b ex4 --display "Booting..."
wd-control-tool -b ex4 -r write                        # push the host's current time to the MCU RTC

wd-control-tool -b ex2 --led power blue on -f 1        # chain several actions in one command, run in order
wd-control-tool -b ex4 -n -D --led power blue on       # preview the exact bytes/registers, touch nothing
```

- `-b`/`--board` selects the board: `ex2u`, `ex2` or `ex4` (required;
  may appear anywhere on the command line, and applies to every
  action in the command - repeating it with a *different* value is a
  usage error, not a way to switch boards partway through one
  command).
- Each action is one of `-l/--led`, `-f/--fan`, `-r/--rtc`,
  `-d/--display`, followed directly by its own fixed number of
  arguments (no delimiter needed - see the table below). Several
  actions can be chained in one command and run in the order given;
  the same action may repeat.
- `-n`, `--dry-run` - print what would be done, touch no hardware.
  May appear anywhere, applies to every action in the command. Exits
  0 whenever the simulation itself completed, even if it reports that
  the real run would fail.
- `-D`, `--debug` - print extra diagnostics (the exact frame bytes
  that would go out the UART, or the exact `/dev/mem` addresses and
  set/clear masks) alongside normal output. Changes nothing about
  what is done.
- `-h`, `--help` / `-v`, `--version` - must be used alone.
- `--serial PATH` overrides the MCU UART device (default
  `/dev/ttyS1`, confirmed on real hardware for EX2/EX4 and, since
  fixing the serial baud-rate bug, for EX2U too - a live `-f 50`
  fan-control test on real EX2U hardware confirmed the fan actually
  responds through this default path).

On a real (non-dry-run) run with multiple actions, the first one that
fails stops the rest - later actions in the same command never run.

## Command reference

| Flag | Boards | Arguments | Notes |
|---|---|---|---|
| `-f`, `--fan` | all | `LEVEL` | Board-native units, NOT a common 0-100 scale: EX2U 0/25/50/75/100 (duty %), EX4 0-4 (Stop..Highest), EX2 0-6 (Stop + 6 fixed RPM steps). See SPEC.md, "Fan control". |
| `-f`, `--fan` | ex2 | `rpm` | Special argument value (not a level): reads back and prints the current fan RPM (`fan rpm = N`, matching the vendor `fan_control -g`'s own wording) - hardware-confirmed, see SPEC.md's "Fan status read-back". |
| `-l`, `--led` | ex2, ex4 | `ZONE COLOR STATE` | zones: `power`, `hd0`, `hd1` (+`hd2`,`hd3` on EX4); colors: `red`, `blue`; states: `on`, `off`, `blinking` (or `blink`). `power` on EX2 is MCU-driven, not `/dev/mem` (hardware-confirmed - see SPEC.md). `hd0`/`hd1`'s `on`/`off` are hardware-confirmed (a real register-page capture); `blinking` on HD-bay and `hd2`/`hd3` (EX4-only, no hardware available to test) have no confirmed register behavior and are reported as an error rather than guessed, same as `blinking` on EX2's power LED. |
| `-l`, `--led` | ex2u | `STATE` | states: `on`, `off`, `blinking` (or `blink`), `breathing` (or `breathe`) - EX2U's single status LED, not a zone/color scheme. |
| `-d`, `--display` | ex4 | `TEXT` | Writes to the front-panel LCD. Not truncated or paginated at the LCD's own 16-char width - see SPEC.md. Taken verbatim, even if it looks like a flag (e.g. starts with `-`). Max 255 bytes (the wire format's own LEN field is one byte wide), and may not contain a literal 0xFA/0xFB byte or be exactly 250/251 bytes long (the wire protocol's own start/end markers - see SPEC.md); any of these is rejected with an error, not silently sent. |
| `-r`, `--rtc` | ex4 | `write` | Pushes the host's current time to the MCU RTC. **EX2 not yet implemented** (frame layout unconfirmed); **no board supports reading the RTC back** yet (see SPEC.md). |
| `-t`, `--temp` | ex2u | (none) | Sends the `tmp_get` request and prints `Temperature: N C` (hardware-confirmed from one live capture - see SPEC.md's "EX2U protocol"). Prints "no reply" rather than failing if nothing arrives within 2s. Use `-D` to see the raw reply bytes (`debug: reply=...`), same as any other command's `-D` output. |

No generic "all boards" fallback exists anywhere: every command's
actual mechanism differs per board (see SPEC.md), and this tool
never pretends otherwise.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | argument/usage error |
| 2 | not supported on this board (e.g. `display` on EX2) |
| 3 | hardware I/O error (couldn't open `/dev/mem` or the serial port, or a register/UART write failed) |

## Build

See `BUILDING.md` for the full toolchain setup and cross-compile
recipe. Summary: Go, `CGO_ENABLED=1` with a cross `gcc` so the result
is dynamically linked against the target's own glibc (no vendor `.so`
dependency at all - just `libc.so.6`/`ld-linux*.so`), stripped
(`-s -w`), targeting `GOARCH=arm` with the right `GOARM` and float ABI
per board:

```sh
make build-ex2u   # armv7 hard-float, for EX2U (and, later, EX2 once its uRamdisk is rebuilt)
make build-ex4    # armv5 soft-float, for EX4 (pending confirmation of its real silicon)
make strip
make test
```

This tool only builds the binary; packaging it into a board's uRamdisk
is out of scope here.

## Author

Symo <symops@gmail.com>
