# wd-control-tool

A self-contained replacement for the proprietary cooler/RTC/LED/
display control stack on three WD My Cloud boards - EX2 Ultra
(EX2U), EX2 Gen2 and EX4 Gen2. Replaces `mcu_ctl` (EX2U) and
`fan_control`/`rtc`/`led`/`up_send_ctl` plus their supporting daemons
(`xmldb`, `up_read_daemon`, `up_send_daemon`) and vendor `.so` stack
(EX2/EX4) with one tool that talks to the hardware directly: the
front-panel MCU over UART (including EX2's power LED - confirmed
MCU-driven, not the GPIO path HD-bay/EX4's power LED and EX2's fan
use), and `/dev/mem`-mapped registers otherwise.

Motivation: get rid of a pile of outdated proprietary libraries and
daemons just to blink an LED or spin a fan - no vendor `.so`, no XML
config daemon, no message-queue relay, no `/tmp` file polling.

See `SPEC.md` for the full, reverse-engineered hardware reference
this implementation follows - including what is still unconfirmed
or unimplemented. Most MCU commands remain fire-and-forget (no reply
ever parsed), but four MCU reads are now confirmed and implemented:
EX2U's temperature (`-t`), EX2's fan RPM (`-f rpm`) and both boards'
RTC (`-r r`/`-r s`) - see SPEC.md, "Request/response and
acknowledgement". EX4 also has its own `-f rpm`, but it's not an MCU
command at all - a direct `/dev/mem` register read (confirmed by
decompiling the real
`libshare.so`), see SPEC.md's "Fan RPM read-back (EX4)".

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
wd-control-tool -b ex2u -l hd0 blue on               # EX2U: hd-bay LED (plain Linux LED-class sysfs)

wd-control-tool -b ex2 -f 3                           # EX2: 6521rpm step (hardware-confirmed - see SPEC.md's "Fan control")
wd-control-tool -b ex2 --led power blue on

wd-control-tool -b ex4 -f 2                            # EX4: Medium
wd-control-tool -b ex4 --led power blue on
wd-control-tool -b ex4 --display "Booting..."
wd-control-tool -b ex4 --sleep on                      # EX4: put the LCD to sleep (dark)
wd-control-tool -b ex4 -r w                            # push the host's current time to the MCU RTC
wd-control-tool -b ex2 -r r                            # read the MCU RTC and print it
wd-control-tool -b ex2 -r s                            # read the MCU RTC and set the host's system clock from it

wd-control-tool -b ex2 --led power blue on -f 1        # chain several actions in one command, run in order
wd-control-tool -b ex4 -n -D --led power blue on       # preview the exact bytes/registers, touch nothing
```

- `-b`/`--board` selects the board: `ex2u`, `ex2` or `ex4` (required;
  may appear anywhere on the command line, and applies to every
  action in the command - repeating it with a *different* value is a
  usage error, not a way to switch boards partway through one
  command).
- Each action is one of `-l/--led`, `-f/--fan`, `-r/--rtc`,
  `-d/--display`, `-t/--temp`, `-s/--sleep`, followed directly by its
  own fixed number of arguments (no delimiter needed - see the table
  below).
  Several actions can be chained in one command and run in the order
  given; the same action may repeat.
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
| `-f`, `--fan` | ex2, ex4 | `rpm` | Special argument value (not a level): reads back and prints the current fan RPM (`fan rpm = N`, matching the vendor `fan_control -g`'s own wording) - hardware-confirmed on both boards, see SPEC.md's "Fan status read-back" (EX2, via the MCU) and "Fan RPM read-back (EX4)" (EX4, a direct `/dev/mem` register read instead - decompiled from the real `libshare.so`, not the MCU). |
| `-l`, `--led` | ex2, ex4 | `ZONE COLOR STATE` | zones: `power`, `hd0`, `hd1` (+`hd2`,`hd3` on EX4); colors: `red`, `blue`; states: `on`, `off`, `blinking` (or `blink`). `power` on EX2 is MCU-driven, not `/dev/mem` (hardware-confirmed - see SPEC.md). EX2's `hd0`/`hd1` `on`/`off` are hardware-confirmed (a real register-page capture); `blinking` on EX2's power LED (both colors) and on EX2's HD-bay RED specifically (hd0/hd1, via a dedicated `hdBayBlinkEn` register) is also hardware-confirmed - HD-bay BLUE's `blinking` is confirmed NOT to work (the vendor's own command was tested directly on real hardware) and is reported as an error rather than assumed to match red (see SPEC.md). **EX4's HD-bay mechanism is different from EX2's** (a separate register pair for red, confirmed per-bay; a single register SHARED by all four bays for blue - the vendor's own code ignores which bay was asked for) - hardware-confirmed and decompiled, see SPEC.md's "HD-bay". On EX4, `hd0`..`hd3` red `on`/`off`/`blinking` all work; blue `on`/`off` work but affect all four bays together, not just the one named; blue `blinking` is a confirmed no-op in the vendor's own code and is reported as an error. |
| `-l`, `--led` | ex2u | `STATE` | states: `on`, `off`, `blinking` (or `blink`), `breathing` (or `breathe`) - EX2U's single MCU-driven status LED, not a zone/color scheme. |
| `-l`, `--led` | ex2u | `hd0\|hd1 COLOR STATE` | EX2U's hd-bay LEDs - hardware-confirmed (the real `/sys/class/leds/wdmcex2u:<color>:hdd<N>/brightness` sysfs path, not MCU/UART or `/dev/mem`; see SPEC.md's "LED (EX2U hd-bay)"). Zones: `hd0`, `hd1` only; colors: `red`, `blue`; states: `on`, `off` only - `blinking` has no confirmed sysfs trigger and reports an error rather than assuming one exists. Told apart from the status-LED form above by the very next word alone (`hd0`/`hd1` selects this form). |
| `-d`, `--display` | ex4 | `TEXT` | Writes to the front-panel LCD. Not truncated or paginated at the LCD's own 16-char width - see SPEC.md. Taken verbatim, even if it looks like a flag (e.g. starts with `-`). Max 255 bytes (the wire format's own LEN field is one byte wide), and may not contain a literal 0xFA/0xFB byte or be exactly 250/251 bytes long (the wire protocol's own start/end markers - see SPEC.md); any of these is rejected with an error, not silently sent. Cyrillic text is automatically transliterated to Latin first (e.g. "Привет" -> "Privet") - the LCD's own character ROM has no Cyrillic glyphs at all (confirmed hardware-only limitation, the vendor's own tool shows the same garbling); `-D`/`--debug` shows the substitution when it happens. |
| `-r`, `--rtc` | ex2, ex4 | `w` | Pushes the host's current time to the MCU RTC, converted to UTC first - the RTC stores UTC, not local time (confirmed on real hardware, see SPEC.md's "RTC"; `-D`'s frame preview reflects the UTC value, not the host's local one). Prints `rtc: system time: YYYY/M/D Mon HH:MM:SS`, matching the vendor `rtc -w`'s own wording - no ack from the MCU either way, this is fire-and-forget on both boards. |
| `-r`, `--rtc` | ex2, ex4 | `r` | Reads the MCU RTC and prints it in UTC, unconverted - matching the vendor `rtc -r`'s own wording exactly (`rtc: RTC time = YYYY/M/D Mon HH:MM:SS`) - hardware-confirmed on both boards. The printed weekday always comes from the date itself (year/month/day), never the wire's own dow byte - see SPEC.md's "RTC" for why that matters specifically on EX4 (its vendor tool prints the wrong weekday for a correctly-written date; this tool doesn't inherit that). |
| `-r`, `--rtc` | ex2, ex4 | `s` | Like `r`, and also sets the host's own system clock from the result (correctly, as an absolute UTC moment - the host's own local timezone is applied automatically, same as any other system-clock write) - hardware-confirmed for the read on both boards; the system-clock-set side effect is not exercised by this project's own automated tests (see HISTORY.md). |
| `-t`, `--temp` | ex2u | (none) | Sends the `tmp_get` request and prints `Temperature: N C` (hardware-confirmed from one live capture - see SPEC.md's "EX2U protocol"). Prints "no reply" rather than failing if nothing arrives within 2s. Use `-D` to see the raw reply bytes (`debug: reply=...`), same as any other command's `-D` output. |
| `-s`, `--sleep` | ex4 | `on`/`off` | Puts the front-panel LCD to sleep (dark) or wakes it back up - hardware-confirmed (a live capture of the vendor's own `up_send_ctl LCDSleepMode 0/1`, see SPEC.md's "LCD sleep mode"). `on` = sleep (dark), `off` = awake (lit) - matches the vendor's own "0: OFF, 1: ON" wording about the sleep feature itself, not the screen. |

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
recipe (and `HISTORY.md` for the project's own testing history).
Summary: Go, `CGO_ENABLED=1` with a cross `gcc` so the result
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
