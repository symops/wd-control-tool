# wd-control-tool specification

This document is the hardware reference wd-control-tool implements
against. Every claim below was reverse-engineered from the real
vendor binaries (extracted from each board's own rescue `uRamdisk`)
via disassembly (`objdump`/`readelf`, plus direct byte extraction from
the compiled command tables - see "Extraction method"), not from any
vendor documentation. Confidence varies per section; each one says so
explicitly. Where a claim was additionally confirmed on real hardware
(live `strace`/register captures, or an end-to-end kernel-driver
test), that is called out explicitly at the point it applies -
substantial parts of EX2 now are (power LED, HD-bay LED, fan level
order, fan RPM read-back - each found the hard way, often after an
earlier "statically decoded" guess turned out wrong on real
hardware); EX4 and the rest of EX2's own protocol remain only
statically decoded, no hardware available to test them.

**Never guess a frame layout you don't have evidence for.** Where this
document says a command's wire format is not confirmed, wd-control-
tool does not implement it; adding it needs either a complete
disassembly of the specific handler or a live UART capture.

## Board summary

| Board | SoC class | Fan | LED | RTC | Display |
|---|---|---|---|---|---|
| EX2 Ultra (EX2U) | Armada 385 (armv7) | MCU/UART | MCU/UART (status LED only; hd0/hd1 bay LEDs are plain kernel LED-class sysfs, out of scope) | not used by vendor `/init`, out of scope | none |
| EX2 | Kirkwood-class (armv5 ABI today, armv7 silicon - see README) | direct `/dev/mem` register write | direct `/dev/mem` register write | MCU/UART (write side unconfirmed) | none |
| EX4 | Kirkwood-class (armv5 ABI) | MCU/UART | direct `/dev/mem` register write | MCU/UART (write side confirmed) | MCU/UART |

## Extraction method

Binaries were extracted from each board's uRamdisk (`mkimage`-wrapped
gzip cpio; strip the 64-byte u-boot header, `gunzip`, `cpio -idm`).
ARM EABI5 binaries are dynamically linked and **stripped of local
symbols only** - dynamic symbols (library exports like
`LIB_Write_CPU_Register`) and all string literals survive, which is
why `objdump -d` + `readelf` + manual literal-pool resolution was
sufficient without a decompiler for most of this.

The EX2/EX4 command table (section "EX2/EX4 protocol") was extracted
directly from `up_send_ctl`'s `.data` section as raw bytes - not from
decompiled pseudocode - by locating the table's known virtual address
via its disassembled dispatch loop, computing the file offset via the
containing section's `Addr`/`Off` (`readelf -S`), then unpacking it as
a flat array of 10 little-endian `u32` words per row (stride 40
bytes). This is the most reliable evidence this document has: raw
compiled data, not a human- or AI-reconstructed interpretation of it.

## EX2U protocol (Welltrend 6703F-OG240WT MCU, uart1)

**Hardware-confirmed** (live `strace` capture + end-to-end kernel
driver test - this is the one protocol in this document verified
against real hardware, not just statically).

- 19200 baud, 8N1, no parity, no flow control.
- Frame: 7 bytes, `FA <cat> <subcmd> <param> 00 00 FB`, sent one byte
  at a time with **no deliberate inter-byte delay**. A live `strace
  -tt` capture of the real, working `mcu_ctl fan_set_50` on actual
  EX2U hardware shows seven single-byte `write()` calls roughly
  0.4-0.6ms apart - pure syscall/`O_SYNC` overhead, no `usleep`/
  `mdelay` in between. **Corrected** after a real-hardware failure
  report: an earlier version of this spec (and of
  `wd-control-tool`'s own `pkg/mcu/ex2u.go`) claimed ~100ms pacing as
  "the real hardware's own margin" - that was never actually
  strace-confirmed, and sending with it (200x slower than the real
  tool) let every `write()` succeed with no I/O error while the
  Welltrend MCU's own frame-reception state machine most likely
  timed out between bytes and never saw a complete frame - no error
  surfaced anywhere, the fan simply never responded. Lesson for this
  spec specifically: "hardware-confirmed" above refers to the frame
  *bytes* and baud/framing (confirmed via the kernel driver
  end-to-end test); the pacing figure had silently drifted into an
  unconfirmed guess despite sitting right next to that claim.
  **Update**: removing the pacing alone did not fix the real-hardware
  symptom on retest - it was a second, independently real bug, not
  the actual root cause. See `pkg/mcu/serial.go`'s `Open` for the
  actual one: `TCSETS` (what this tool uses, matching the vendor
  tool) encodes the baud rate entirely in `c_cflag`'s `CBAUD` bits,
  not in separate `c_ispeed`/`c_ospeed` fields - `wd-control-tool`
  was setting only the latter (ignored by `TCSETS`), leaving every
  port at the kernel's default `B0` (0 baud) regardless of the
  `baud` argument passed to `Open`. A live `strace -x` comparison on
  real EX2U hardware caught it directly: this tool's own
  `TCSETS` showed `c_cflag=B0|CS8|CREAD|CLOCAL`, the vendor
  `mcu_ctl`'s showed `c_cflag=B19200|CS8|CREAD|CLOCAL`. Fixed by
  OR'ing the baud constant into `Cflag` itself; regression-tested by
  reading the configured termios back with `TCGETS` and checking
  `Cflag&CBAUD` directly (`TestOpen_SetsRealBaudRateInCflag` -
  deliberately not a PTY-based test, since a pty has no real bit
  rate to get wrong).
- No checksum.

| Command | Bytes |
|---|---|
| `led_set_on` | `FA 03 06 01 00 00 FB` (confirmed via `wd-control-tool -b ex2u -l on` on real hardware) |
| `led_set_off` | `FA 03 06 00 00 00 FB` (confirmed via `wd-control-tool -b ex2u -l off` on real hardware) |
| `led_blink` | `FA 03 06 02 00 00 FB` (confirmed via `wd-control-tool -b ex2u -l blinking` on real hardware) |
| `led_breath` | `FA 03 06 04 00 00 FB` (confirmed via `wd-control-tool -b ex2u -l breathing` on real hardware) |
| `sys_ready` | `FA 03 01 00 00 00 FB` |
| `sys_silent` | `FA 03 03 00 00 00 FB` |
| `sys_shutdown` | `FA 03 03 01 00 00 FB` (confirmed genuine hard power cut, not cosmetic) |
| `tmp_get_f`/`tmp_get_c` | request: `FA 03 08 00 00 00 FB`; reply: `FA 03 08 00 00 <F> FB` where `<F>` is the temperature in whole-degree Fahrenheit - confirmed via two live captures (`wd-control-tool -b ex2u -t` alongside a simultaneous `mcu_ctl tmp_get_c` on real hardware): `0x56`=86 decimal alongside "Temperature: 30 C", and separately `0x54`=84 decimal alongside "Temperature: 28 C". `tmp_get_c`'s F-to-C conversion **truncates** rather than rounds ((F-32)*5/9 as plain C-style integer division): the first capture couldn't distinguish the two (86°F is exactly 30°C either way), but the second does - 84°F rounds to 29 but truncates to 28, and the vendor reported 28. The reply's 8th byte in both captures was a trailing `0xFA`, not yet explained - see `pkg/mcu/ex2u.go`'s `EX2UDecodeTempFahrenheit`/`EX2UDecodeTempCelsius`. |
| `fan_set_0/25/50/75/100` | `FA 02 00 <00/40/80/c0/ff> 00 00 FB` |
| `sys_reboot` | **not captured, not needed** - this feature isn't wanted; do not guess the bytes if that ever changes |

## EX2/EX4 protocol ("legacy" MCU, `/dev/ttyS1`)

Statically decoded only (not hardware-confirmed). Table-driven,
`FA...FB` framed, **no checksum anywhere** (checked across every
handler shape below). Richer than EX2U's: several frame shapes, and
variable length for RTC-bulk-write and free text.

### UART line settings

Confirmed per-model from the `RS232_SetParam` call site - **do not
assume parity between boards**:

| Board | Baud | Data | Parity | Stop |
|---|---|---|---|---|
| EX4 | 19200 | 8 | N | 1 |
| EX2 | 115200 | 8 | N | 1 |

Inter-byte pacing: the vendor `up_send_daemon` used `usleep(10us)`
between bytes - negligible next to either baud rate's own bit time
(~520us/byte at 19200, ~87us/byte at 115200), so not load-bearing, but
reproduced for parity.

### Command dispatch

`up_send_ctl <Name> [args...]` looks `Name` up in a flat table (EX4:
82 rows at file offset `0x609c` in the binary's `.data`; EX2: 56 rows
at `0x2370`), stride 40 bytes = 10 LE `u32` words:

```
word0 = name (char*, resolved via .rodata)
word1 = packed bytes b4,b5,b6,b7 (b4 = LSB .. b7 = MSB)
word2 = "const" payload byte, used by fixed-shape commands
word3 = dispatch group - which frame-builder/handler to use
word4..word9 = mostly unused; word6/word7 hold write-range min/max for single-field RTC writes (group 3)
```

Frame shapes, by dispatch group (only the ones this tool implements
are described in full; see "Full command tables" for every row):

- **group 1 (fixed, no argument)**: `FA b4 b5 b6 b7 const FB` (7
  bytes). Covers reads (`RDateAndTime` on EX4) and most no-argument
  writes (`DeviceReady`, `FanStop`/`FanHalf`/`FanFull` on EX2, etc).
- **group 24 (EX4 `FanControl` only)**: table's `b6` slot is replaced
  at runtime by the value argument: `FA b4 b5 VALUE b7 const FB`.
  Worked example - `up_send_ctl FanControl 3` (row: b4=02,b5=00,b7=00,
  const=00): **`FA 02 00 03 00 00 FB`**.
- **group 2, write side (`WDateAndTime`)**: `FA b4 b5 b6 b7 <7 BCD
  fields: sec,min,hour,dow,dom,mon,year> FB` (13 bytes). Field order on
  the wire is **reversed** from the CLI's `year mon dom dow hour min
  sec` order. BCD packing: `((v/10)<<4)|(v%10)`. Worked example -
  `up_send_ctl WDateAndTime 2026 10 31 6 14 5 30` (EX4 row: b4=01,
  b5=00,b6=04,b7=07): **`FA 01 00 04 07 30 05 14 06 31 10 26 FB`**.
  EX2's `RDateAndTime`/`WDateAndTime` share this same dispatch group
  number (2) but NOT the same header bytes - **now confirmed too**
  via a live `rtc -w` capture (b4=01,b5=08,b6=02,b7=07 - see "RTC"
  below for the full worked example and a correction to this table's
  own b7 column for this row). Same 13-byte shape otherwise, same BCD
  field order.

  The BCD formula above is `((v/10)<<4)|(v%10)` for `v` already known
  to be in its field's valid range (confirmed empirically, not just by
  reading the formula: a negative `v` can produce a byte equal to this
  protocol's own `0xFA`/`0xFB` markers - e.g. `v=-5` -> `0xFB`, `v=-6`
  -> `0xFA` - corrupting the frame). `wd-control-tool`'s
  `EX4DateTimeFrame` validates every field (year >= 0, month 1-12, day
  1-31, weekday 0-6, hour 0-23, minute/second 0-59) before it ever
  reaches the BCD step, for exactly this reason.
- **group 2, `WMAlarmDateAndTime` (EX4 only)**: `FA b4 b5 b6 b7 <5 BCD
  fields: mon,dom,hour,min,sec> FB` (11 bytes).
- **group 9 (`SysName`, EX4 only)**: `FA b4 b5 b6 LEN <text, raw
  ASCII, LEN bytes, no NUL> FB`. **The protocol does not truncate or
  paginate at 16 characters** - the 16-char, `>`-terminated strings
  seen in the vendor `/init` script were manually pre-truncated by
  whoever wrote that script, not enforced by this layer. How an
  over-length payload behaves on real LCD firmware is untested. The
  wire format itself DOES impose a hard ceiling of 255 bytes, though,
  simply because LEN is one byte wide - `wd-control-tool`'s own
  `EX4DisplayFrame` rejects text over 255 bytes with an error rather
  than what an earlier version of it did: silently compute
  `byte(len(text))`, which wraps to 0 for exactly 256 bytes, producing
  a frame that lies about its own payload length (see git history for
  the fix - this is the kind of implementation bug this spec can't
  capture by describing the wire format alone). Separately,
  `EX4DisplayFrame` also rejects any text whose bytes - or whose LEN
  byte, `byte(len(text))` itself - equal `0xFA`/`0xFB`: this tool has
  never decoded the MCU's receive-side parsing loop (see "Unresolved"
  #1 below), so it does not know whether the firmware trusts LEN for
  a fixed-size read or re-scans for the end marker, and either way a
  literal marker byte inside the text (or a 250/251-byte text, whose
  LEN byte alone is `0xFA`/`0xFB`) would corrupt the frame. `--display`
  takes text "verbatim" at the shell level (it isn't treated as a
  flag even if it looks like one), but `os.Args` bytes aren't
  guaranteed valid UTF-8, so a crafted argument (e.g. `$'\xfa\xfb'`,
  or a 250-byte string) really can hit this.

  **Cyrillic text shows as garbage on the physical LCD - confirmed to
  be the panel's own font ROM, not this protocol.** A real-hardware
  report showed `wd-control-tool -b ex4 -d "Привет"` sending a
  perfectly well-formed frame (`FA 04 01 00 0C D0 9F D1 80 ... FB` -
  the raw UTF-8 bytes of "Привет", exactly as given) but displaying
  unreadable characters on the LCD - and the vendor's OWN `sysname`
  tool shows the identical garbling for the identical text, on the
  identical hardware. This rules out a protocol/encoding bug on this
  tool's side: there is no byte sequence this tool could send instead
  that the LCD would render as a Cyrillic letter, because the LCD's
  own character ROM (a typical HD44780-style fixed font table) was
  never built with Cyrillic glyphs in it at all - a hardware
  limitation of the panel itself, same as the vendor's own tool hits.
  `wd-control-tool` works around this at the CLI layer
  (`transliterateCyrillic` in `cmd/wd-control-tool/translit.go`),
  converting each Russian Cyrillic letter to a readable Latin
  transliteration (e.g. "Привет" -> "Privet") before building the
  frame - `EX4DisplayFrame` itself is unchanged and knows nothing
  about this; it's purely a CLI-level text transform, applied
  automatically (no flag needed), with a `-D`/`--debug` line showing
  the substitution whenever it changes the text.
- **group 8 (`DeviceShutdown`/`MCUReset`/`DeviceShutdownByUPS`)**: `FA
  b4 b5 b6 b7 VALUE FB` - the final byte is a **runtime** value, not a
  table constant. `MCUReset` in particular requires a numeric argument
  on the real tool (`up_send_ctl MCUReset <code>`); the meaning of
  `<code>` was not decoded, so `wd-control-tool` cannot validate its
  real range - it only rejects `VALUE` being exactly `0xFA`/`0xFB`
  (the frame's own start/end markers), since that would corrupt the
  frame structure regardless of what the code means semantically.

### Full command tables

Extracted byte-for-byte as described in "Extraction method". `grp` is
the dispatch group (word3); `const`/rest are the table's remaining
words, included for anyone extending this spec. This is the complete
table, not just the rows wd-control-tool currently wires up.

**EX4 (82 rows)** - selected rows actually used by wd-control-tool
(`pkg/mcu/ex4.go`); the complete 82-row dump (including RTC
individual-field commands, LCD/volume-status commands, WOL, PHY
register pass-through, etc - none of these are wired up yet) is kept
in `test/data/ex4_commands.txt` for anyone extending this tool:

| Name | b4 | b5 | b6 | b7 | const | grp |
|---|---|---|---|---|---|---|
| FanControl | 02 | 00 | 00 | 00 | 00 | 24 |
| RDateAndTime | 01 | 00 | 03 | 01 | 00 | 1 |
| WDateAndTime | 01 | 00 | 04 | 07 | 00 | 2 |
| WMAlarmDateAndTime | 01 | 00 | 06 | 05 | 00 | 2 |
| SysName | 04 | 01 | 00 | 00 | 00 | 9 |
| DeviceReady | 03 | 01 | 00 | 00 | 00 | 1 |
| DeviceShutdown | 03 | 03 | 01 | 01 | 00 | 8 |
| MCUReset | 03 | 03 | 02 | 01 | 00 | 8 |
| DeviceShutdownByUPS | 03 | 03 | 03 | 01 | 00 | 8 |

**EX2 (56 rows)** - selected rows actually used
(`pkg/mcu/ex2.go`); full dump in `test/data/ex2_commands.txt`:

| Name | b4 | b5 | b6 | b7 | const | grp |
|---|---|---|---|---|---|---|
| FanStop | 02 | 00 | 00 | 00 | 00 | 1 |
| FanHalf | 02 | 00 | 01 | 00 | 00 | 1 |
| FanFull | 02 | 00 | 02 | 00 | 00 | 1 |
| DeviceReady | 03 | 01 | 00 | 00 | 00 | 1 |
| ThermalGet | 03 | 08 | 00 | 00 | 00 | 1 |
| FanStatusGet | 03 | 09 | 00 | 00 | 00 | 1 |
| RDateAndTime | 01 | 08 | 01 | 01 | 00 | 2 |
| WDateAndTime | 01 | 08 | 02 | 01\* | 00 | 2 |

\* WDateAndTime's b7 as extracted from the static table (same value
as RDateAndTime's row) is NOT what's actually transmitted: a live
`rtc -w` capture shows the real wire byte at that position is `07`
(see "RTC" below) - the write-side handler overrides this field at
runtime, the same way `buildValueFrame` overrides B6 for other
commands. RDateAndTime's own b7=`01` IS what's transmitted (it's a
fixed request frame, no override) - confirmed by the same capture.

Note EX2's own fan commands (`FanStop`/`FanHalf`/`FanFull`, dispatch
group **1**, fixed shape) are a *separate* control surface from the
one `fan_control -f N` actually uses at boot (see "Fan control"
below) - EX2's real `LIB_Set_Fan_Speed` never touches this UART
channel at all for *setting* speed, only for *reading* sensors
(`ThermalGet`/`FanStatusGet`).

### Request/response and acknowledgement

The vendor architecture has **no generic, opcode-based response
dispatch**. `up_send_ctl` itself is fire-and-forget for every shipped
command (there is dead code for a generic synchronous-wait helper, but
no table row currently uses the dispatch groups that would reach it).
Acknowledgement/response was handled by two other vendor processes
this tool deliberately does not reimplement or depend on:

- `up_send_daemon` (the writer) polled a shared "XML DB" group
  (`/runtime/mcu/ack`) for the literal string `"ack"` after sending,
  via `libxmldbc.so` - not reverse-engineered.
- `up_read_daemon` (the reader) parsed MCU replies off the UART and
  published them into named XML-DB groups (`/runtime/rtc`,
  `/runtime/themister/...`, etc) that callers like the vendor `rtc`
  tool polled with their own retry policy (300ms, up to 11 retries).
  **The MCU-to-host reply frame format itself was never decoded** -
  neither its own `FA...FB`-or-otherwise framing nor how one reply is
  matched to the request that triggered it.

**Consequence for wd-control-tool**: almost every command in this
tool is fire-and-forget, with no read-back or acknowledgement - a
known, deliberate limitation, not an oversight (see "Unresolved").
Three exceptions, all added later via a live capture rather than
decoding the general mechanism above: EX2U's temperature (`-t`,
"EX2U protocol"), EX2's fan RPM (`-f rpm`, "Fan status read-back")
and EX2's RTC read (`-r r`/`-r s`, "RTC") - each decodes one specific
reply shape, not the general reply-framing problem described above,
which remains unsolved for everything else. The first two share
their own request's 7-byte shape; `RDateAndTime`'s reply instead
shares `WDateAndTime`'s 13-byte shape (a *different* command's
request frame, header echoed back verbatim) - the one case so far
where a reply's shape wasn't simply "my own request, answer in byte
5".

## Fan control

- **EX2U**: MCU/UART only, 5 duty-cycle steps (0/25/50/75/100),
  hardware-confirmed.
- **EX4**: MCU/UART only for **setting** speed (`FanControl`, levels
  0-4 = Stop/Low/Medium/High/Highest, a direct passthrough of
  `fan_control -f N`'s own argument - confirmed via the vendor
  binary's own identity lookup table, and confirmed working via
  `wd-control-tool -b ex4 -f 0..4` directly on real EX4 hardware).
  **Reading** RPM is a completely different mechanism, NOT the MCU at
  all - see "Fan RPM read-back (EX4)" below.
- **EX2**: **direct physical-register writes, no MCU involved at
  all** for setting speed (confirmed: EX2's `LIB_Set_Fan_Speed` never
  calls `up_send_ctl`/touches the UART). Registers (base `0xf1000000`,
  the same Kirkwood-class "internal registers" window LED uses):
  - `0xf1018100` ("reg1"): one-hot bits `0x40`/`0x100`/`0x800`/
    `0x1000`/`0x10000`.
  - `0xf1018140` ("reg2"): one-hot bit `0x1` - this is the EXACT SAME
    physical register as HD-bay LED's `hdBayReg1` (see "LED
    (EX2/EX4)" below), not merely the same window at a different
    offset: the two subsystems share this one register on entirely
    disjoint bits (fan: bit 0 only; HD-bay LED: bits 20/21/23/25),
    safely, via atomic read-modify-write.
  - Clearing mask confirmed as `0x11940` (reg1) / `0x1` (reg2) -
    exactly the union of the bits above; "stop" clears every fan bit.
  - `fan_control -f N` on EX2 (`N` = 0-6; **7 and 8 clamp to 6's
    behavior on real hardware**, confirmed via the vendor tool
    itself - the valid range really is 0-6, not wider) maps to
    `0`=Stop + 6 fixed RPM steps.
  - **Level-to-bitmask order: CORRECTED, confirmed on real hardware**
    via the vendor `fan_control`'s own `-g` RPM read-back (see "Fan
    status read-back" below), not just relative "feels faster/
    slower" - an earlier version of this spec (and of
    `wd-control-tool`'s own `pkg/fan/ex2.go`) inferred the order from
    the jump table's natural case order, which was WRONG (confirmed
    the hard way: level 2 measurably spun faster than level 3). The
    real order, each level's bit read back after setting it:

    | Level | Register:bit | RPM |
    |---|---|---|
    | 1 | reg1:`0x800` | 4411 |
    | 2 | reg1:`0x1000` | 5357 |
    | 3 | reg1:`0x10000` | 6521 |
    | 4 | reg1:`0x100` | 7317 |
    | 5 | reg2:`0x1` | 8333 |
    | 6 | reg1:`0x40` | 9090 |

    (~1000rpm per step, matching the ~4000-9000rpm range this spec
    already documented - strong corroboration, not a coincidence.)
  - EX2 *also* exposes `FanStop`/`FanHalf`/`FanFull` over the legacy
    MCU protocol (see table above) - a separate, apparently-unused-by-
    `fan_control` control surface. wd-control-tool does not currently
    call it.

### Fan status read-back (EX2 `FanStatusGet` reply - CONFIRMED)

Unlike most MCU replies in this spec (see "Unresolved" below for the
ones that remain undecoded) - alongside EX2U's `tmp_get` reply above,
the other confirmed exception - EX2's `FanStatusGet` reply is now
decoded: a live strace of the real
`fan_control -g 4` printed `fan rpm = 5172` while the captured reply
was `FA 03 09 00 00 3A FB` (`0x3A` = 58 decimal). Byte 5 is a
tachometer **period**, not RPM directly: `300000/58 = 5172.41`,
truncating to exactly `5172` (plain C-style integer division, same
truncation behavior as `tmp_get_c`'s F-to-C conversion on EX2U).
Cross-confirmed against the 6 RPM values in the level table above
(no raw byte captured for those, but every single one round-trips
exactly through `period = round(300000/rpm)`, `300000/period`
truncated - would be an extraordinary coincidence otherwise). See
`pkg/mcu/ex2.go`'s `EX2DecodeFanRPM`; `wd-control-tool -b ex2 -f rpm`
exposes this.

### Fan RPM read-back (EX4 - CONFIRMED, direct register, not the MCU)

A live strace of the real `fan_control -g 4` on real EX4 hardware
showed it never sends anything resembling a read request over
`/dev/ttyS1` at all - the only UART traffic during the whole command
is a single `FA 04 83 00 07 "1980rpm" FB` write, which is EX4's
`SysName`-style text-display frame (`FA b4 b5 b6 LEN <text> FB`,
`LEN`=`0x07`="1980rpm", 7 ASCII bytes) writing the ALREADY-KNOWN
answer to the front-panel LCD as a courtesy - not a request for it.
The real value comes from repeatedly `open()`+`mmap()`+`close()`-ing
`/dev/mem` (confirmed in the same strace: dozens of `openat("/dev/
mem", ...)`+`mmap2(NULL, 4096, ..., 0xf1010000)`+`close()` cycles,
same page-at-a-time pattern `pkg/regio` already uses for EX2's fan/
both boards' LEDs), not the legacy MCU protocol.

Decompiling the real `libshare.so` (Ghidra, confirmed against the
same `0xf1010000` page the strace showed) gives the exact mechanism:

```c
getFanStatus()  = (LIB_Read_CPU_Register(0x10150) >> 7) & 1
getFanPluse()   = count of 0/1 transitions of that bit, polled as
                  fast as possible for ~1 real second (a tight
                  busy-loop, each iteration re-opening/mmapping
                  /dev/mem) - exactly one edge over the whole window
                  is treated as 0 (can't represent a full tachometer
                  cycle)
getFanrpm()     = pulses < 2 ? 0 : pulses * 15
```

`LIB_Read_CPU_Register(0x10150)` reads physical address
`0xf1000000 + 0x10150 = 0xf1010150` - confirmed matching the
`0xf1010000`-page mmap the strace showed (`0x10150`'s low 12 bits,
`0x150`, is the in-page offset). The `*15` constant matches the
standard 2-pulse-per-revolution tachometer convention (4 edges/rev:
`RPM = edges * 60 / 4 = edges * 15`) and was cross-validated against
3 real captures, not just the decompiled formula alone: `fan rpm` =
2010/1980/2430 all divide evenly by 15 (134/132/162 edges).
`wd-control-tool -b ex4 -f rpm` reimplements this exactly (clean-room,
no vendor code linked) in `pkg/fan/ex4.go`'s `EX4ReadFanPulses`/
`EX4DecodeFanRPM`, via `pkg/regio` (not a new mechanism - the same
one EX2's fan/both boards' LEDs already use).

Both EX2 and EX4 read other sensors too (`ThermalGet`/
`GetThemister`), but **this tool cannot parse those replies** (see
"Request/response" above) - only `FanStatusGet`'s RPM reply has been
decoded so far.

**Unsolicited frame on the wire, confirmed via `-D`/`--debug`**: real
EX2 hardware was found to send a `FA 30 00 00 00 00 FB` frame
independent of request/reply timing - sometimes right after this
tool's own reply, sometimes before it, sometimes a previous
invocation's never-read leftover glued to the front of the next
one's reply (the kernel's own receive-buffer queue for the tty
persists across separate opens of the same device). `b4=0x30` is not
a command this spec's EX2 table lists under that byte, so its actual
meaning/purpose is unknown - not investigated further since it
doesn't block anything this tool implements. Handled by
`(*Port).flushInput` (drains stale bytes right before sending a
request) and `findFrame` (scans for the matching reply rather than
assuming it's at the front) in `pkg/mcu/serial.go` - see either's own
doc comment for the exact captured byte sequences that justified
this.

## LED (EX2/EX4)

Two genuinely different mechanisms, not one - found out the hard way
on real EX2 hardware (see below).

### HD-bay (hd0..hd3) - `/dev/mem` GPIO, confirmed on real hardware

**Direct `/dev/mem` register pokes against a Marvell-style SoC GPIO
controller, physical base `0xF1000000` - no MCU/UART, no ioctl, no
I2C, no sysfs.** Originally only statically decoded (via
`libshare.so`'s `LIB_Write_CPU_Register`/`LIB_Write_Mem`, the
textbook `/dev/mem` pattern: `open64("/dev/mem", O_RDWR|O_SYNC)`,
`mmap64` the containing page, poke the 32-bit word, `munmap`/
`close`); now **confirmed on real EX2 hardware** too - a live strace
of the real `led` tool for `led hd0/hd1 blue/red on/off` shows exactly
4 `openat(.../dev/mem...)` calls per command (read+write, clear+set -
see "on"/"off" below), matching this mechanism.

**Registers and bits: fully re-derived from a real register-page
capture**, after TWO rounds of wrong guesses. First, a real-hardware
failure report showed neither color on hd0/hd1 had any physical
effect through this tool, despite the vendor's own `led hd0/hd1 ...
on/off` working. A live strace of the real, working `led hd0 red on`
showed its `mmap2` of `/dev/mem` mapping page `0xF1018000`, not
`0xF1010000` - the SAME page `pkg/fan/ex2.go`'s confirmed
`ex2FanReg1`/`ex2FanReg2` (`0xf1018100`/`0xf1018140`) already live
in; the original `0xF1010140`/`0xF1010148` was almost certainly a
single-digit transcription slip (`0x10` for `0x18`). That fix kept
the OLD in-page offsets (`0x140`/`0x148`) and the OLD bit formula
(`13+N`) on the new page - **also wrong**: real hardware still
didn't light up. Rather than guess a third time, a full scan of
every word in the `0xF1018000` page was taken before and after each
real on/off transition for both bays and both colors. The results:

| LED | Reg 1 | Reg 2 | hd0 red | hd0 blue | hd1 red | hd1 blue |
|---|---|---|---|---|---|---|
| HD-bay | `0xF1018140` | `0xF1018150` | bit 21 | bit 25 | bit 20 | bit 23 |

Confirmed facts this capture established, replacing every earlier
assumption:

- **The two registers are NOT a Data-Out/Blink-Enable pair** the way
  Power's two registers are - every single transition changed the
  identical bit in BOTH registers together, never one register
  differently from the other. `wd-control-tool` writes both
  registers identically for `on`/`off`.
- **Red and blue use entirely different, non-overlapping bits** per
  bay - there is no confirmed mutual exclusion between them (unlike
  Power's one bi-color LED), so `on`/`off` only ever touch the
  requested color's own bit, never the other color's.
- **The bit numbers follow no simple formula** - hd0's red-to-blue
  gap is 4 bits, hd1's is 3; bit 13+N (the old guess) matches none of
  the 4 confirmed values. `0xF1018100` (bit 13 = `0x2000`) was a
  coincidence: that register holds a constant `0x00002000` across
  every transition tested - unrelated to LED control.
- **`blinking` is confirmed for RED only (hd0/hd1), not BLUE** - via a
  THIRD register, `hdBayBlinkEn` (`0xF1018148`), separate from both
  `hdBayReg1`/`hdBayReg2`. Found by properly diffing a full
  `0xF1018000`-`0xF1018200` register range across two independent
  real transitions: `led hd0 red on` -> `led hd0 red blinking` (hd0
  already lit) and, separately, straight into `led hd1 red blinking`
  with hd1 never turned on at all. Both transitions changed exactly
  one thing: the bay's own bit in `hdBayBlinkEn` (same bit positions
  as `hdBayBits`' RED column - hd0=`1<<21`, hd1=`1<<20`). `hdBayReg1`
  never changed in either transition - confirmed irrelevant to
  blinking, not just coincidentally unchanged, since hd1's own
  `hdBayReg1` bit was `0` throughout yet the vendor's command still
  worked. `hdBayReg2` DID change, but only as a side effect of the
  hardware blink generator actually driving the pin in real time (the
  same oscillation seen earlier, now understood as a readback of the
  live pin state, not a register whose write controls anything).
  `wd-control-tool` writes only `hdBayBlinkEn`'s bit for `blinking`.
  An EARLIER attempt (based on a weaker capture - reading `hdBayReg2`
  twice during an already-blinking state, with no comparison against
  a known `on` baseline) wrongly concluded `hdBayReg2` itself was the
  control register; that write shipped briefly and had **no actual
  effect** on real hardware, and was reverted before this proper
  capture replaced it. BLUE's own HD-bay blinking is **confirmed NOT
  to work** - the vendor's own `led hd0/hd1 blue blinking` was tested
  directly on real hardware and doesn't work either, so this isn't a
  gap to someday close by assuming it matches red. `wd-control-tool`
  reports an error (`errHDBayBlueBlinkingNotSupportedEX2`).
- **Leaving `hdBayBlinkEn`'s bit set breaks `on`/`off` entirely, for
  RED** - confirmed on real hardware: after a real `led hd0/hd1 red
  blinking`, `wd-control-tool`'s own `on`/`off` had **no effect at
  all**, because the hardware blink generator keeps driving the pin
  (and overriding both `hdBayReg1`/`hdBayReg2`) as long as
  `hdBayBlinkEn`'s bit stays set. `wd-control-tool`'s `on`/`off` now
  also clear the zone's bit in `hdBayBlinkEn` - **RED ONLY**. This was
  originally done for both colors, on the untested assumption that
  clearing an always-0 bit is harmless; a real-hardware report showed
  plain BLUE `on`/`off` (which never touches blinking at all) broke
  the moment that extra write was added for blue too, so
  `hdBayBlinkEn` is NOT a simple independent per-bit register the way
  `hdBayReg1`/`hdBayReg2` are - writing it at all, for a bit position
  (blue's) never confirmed to mean anything there, has a real side
  effect. `wd-control-tool` now only touches `hdBayBlinkEn` for RED -
  confirmed on real hardware to restore plain blue `on`/`off`, though
  only after a full reboot: the bad write had left the hardware
  itself latched in the broken state, not just ineffective while it
  ran, and no amount of further register writes (with the bug already
  fixed in code) brought it back without a power cycle. See
  BUILDING.md for the full story.
- **hd2/hd3 (EX4-only) have no confirmed bits either** - no EX4
  hardware has been available to test them, and the formula that
  would have predicted them was proven wrong for hd0/hd1, so
  `wd-control-tool` reports them as unconfirmed
  (`errHDBayZoneUnconfirmed`) rather than keep guessing with it.

`libalpha_common.so` (also linked by `led`) exports **no**
hardware-access functions at all - its entire ~99-function export
table is xmldb/disk/RAID/volume bookkeeping. The one call `led` makes
into it (`dbSetSocket`) is pure software state-tracking, unrelated to
the physical write.

#### EX4's HD-bay mechanism is DIFFERENT from EX2's - confirmed by live capture + decompilation

A real-hardware report showed `wd-control-tool -b ex4 -l hd0/hd1 red
on` had no physical effect at all, despite running the exact same
writes (`hdBayReg1`/`hdBayReg2` = `0xF1018140`/`0xF1018150`) that are
confirmed on real EX2 hardware above. A second report found something
the EX2 model structurally cannot explain either way: EX4's blue
hd-bay LEDs toggle **all four bays together**, while red toggles each
bay **independently**.

A live strace of the real EX4 `led hdN red/blue on/off` (`-e
trace=openat,mmap,mmap2,pread64,pwrite64,write,close`) found two
entirely different `/dev/mem` pages, one per color, confirmed across
all 16 zone/color/state combinations (hd0..hd3 x red/blue x on/off):

| Color | `/dev/mem` page mapped |
|---|---|
| red | `0xf1010000` - the SAME page EX4's power LED uses (see "Power LED" below), just different registers within it |
| blue | `0xe0010000` - an entirely different physical page, shared identically across every bay |

Decompiling the real `led` binary (the same file already confirmed
for EX4's power LED) confirmed exactly why, via the dispatcher
`FUN_0000acb8(bay_index, color, state)` both colors share:

```c
// red (FUN_00009c9c, hd0 shown - hd1/hd2/hd3 use bits 14/15/16):
// "on":       reg1 |= 0x2000;  reg2 &= ~0x2000;
// "off":      reg1 &= ~0x2000; reg2 &= ~0x2000;
// "blinking": /* reg1 untouched */ reg2 |= 0x2000;
// reg1 = 0xf1010140, reg2 = 0xf1010148 - bits 13/14/15/16 for
// hd0/hd1/hd2/hd3, confirmed directly from the decompiled constants
// (not the old, EX2-disproven "13+N" guess - this happens to match
// that shape, but is independently confirmed, not assumed).

// blue (FUN_0000ab20) - the bay-index argument is read and validated
// (0..3) but then NEVER USED to compute anything:
// "on":  LIB_Write_Mem(0xe00104f0, 0x7c);   // same for ANY bay
// "off": LIB_Write_Mem(0xe00104f0, 0xf44);  // same for ANY bay
// "blinking": no branch at all - a confirmed no-op in the vendor's
// own code, not a gap in this tool.
```

This fully explains the real-hardware report: EX4's blue hd-bay LEDs
share ONE register across all four bays by design in the vendor's own
code, not a bug anywhere in the chain - there is no per-bay blue
control to replicate. Red, in contrast, genuinely is per-bay, just in
a different register pair from EX2's.

`wd-control-tool` replicates this exactly (`pkg/led.EX4HDBayPlan`,
dispatched from `cmd/wd-control-tool/main.go`'s `buildLEDAction` for
`board.EX4` HD-bay zones instead of the EX2-only `Plan`/`hdBayBits`
path): red is per-bay (`on`/`off`/`blinking` all confirmed and
implemented, including the asymmetric "blinking only touches reg2"
detail above); blue `on`/`off` write the single shared register
regardless of which zone was requested (matching the vendor's own
behavior byte-for-byte, surprising as it is); blue `blinking` reports
an error (`errHDBayBlueBlinkingNotImplementedEX4`) rather than
silently doing nothing, since the vendor's own code has no such branch
either. **Confirmed working on real EX4 hardware** by the user
directly after this implementation landed (red and blue hd0..hd3,
including blue's shared-across-all-bays behavior matching what the
decompilation predicted, and red's `blinking` state specifically -
the asymmetric "reg2 only" write confirmed above actually blinks the
real LED, not just a register write with no visible effect).

One capture's own strace log (`hd0_blue_on.log`) initially showed `sh:
strace: not found` with exit status 0 for every single command - not
a hardware problem at all: the vendor's own `/tools` chroot (which
`ex2.sh` execs `led` into) only has busybox, no `strace` binary.
`strace` has to wrap `ex2.sh` itself from the host side (`-f` follows
the fork/exec straight through `ex2.sh`'s own `chroot()` into `led`) -
every other EX4 capture in this document already did this correctly;
this was just the first time the mistake was made explicit by actually
checking a log file's contents instead of trusting a zero exit status.

### Power LED - MCU (`NewPWRLed`), confirmed on real EX2 hardware - NOT `/dev/mem`

**Corrected** after a real-hardware failure report: this spec
previously claimed the power LED used the *same* `/dev/mem` GPIO
mechanism as HD-bay ("confirmed identical... same wrapper functions
in `libshare.so`") - that claim was never actually hardware-confirmed
for the power LED specifically, and turned out to be wrong. A live
strace of the real `led power blue on` on real EX2 hardware opens
**no `/dev/mem` at all** - only `/dev/ttyS1` (via `librs232.so`),
i.e. the legacy MCU protocol (see "EX2/EX4 protocol" above), using
the command this spec previously listed as one of its anomalous,
unexplained table rows: **`NewPWRLed`** (`b4=0x26`, dispatch group
24/`buildValueFrame` shape: `FA 26 00 <value> 00 01 FB`).

`<value>` is a bitmask, confirmed via a live strace capturing all 6
combinations of color x off/on/blinking (the blinking pair added
after a real-hardware report that the vendor's own tool supports it -
previously only off/on had been captured):

| Command | Frame 1 (clear other color) | Frame 2 (set this color) |
|---|---|---|
| `led power blue on` | `FA 26 00 04 00 01 FB` | `FA 26 00 11 00 01 FB` |
| `led power blue off` | `FA 26 00 04 00 01 FB` | `FA 26 00 01 00 01 FB` |
| `led power blue blinking` | `FA 26 00 04 00 01 FB` | `FA 26 00 21 00 01 FB` |
| `led power red on` | `FA 26 00 01 00 01 FB` | `FA 26 00 14 00 01 FB` |
| `led power red off` | `FA 26 00 01 00 01 FB` | `FA 26 00 04 00 01 FB` |
| `led power red blinking` | `FA 26 00 01 00 01 FB` | `FA 26 00 24 00 01 FB` |

This decodes perfectly into: blue bit = `0x01`, red bit = `0x04`, "on"
flag = `0x10`, **"blinking" flag = `0x20`** (a third, separate single
bit - not combined with the on flag). Frame 1 always clears the
*other* color (value = that color's bit alone, no flag - itself a
genuine "turn that color off"); frame 2 sets *this* color to the
requested state (value = this color's bit, OR'd with the matching
state's flag, or no flag at all for off). Unlike Power, HD-bay's two
colors turned out to have NO confirmed mutual exclusion (see "HD-bay"
above - a stale version of this sentence used to claim otherwise,
before that capture). See `pkg/mcu/ex2.go`'s `EX2PowerLEDFrames`.

**Inter-frame pacing between the two frames is load-bearing, not
cosmetic** - found the hard way (again): sending both back-to-back
(this tool's first version) was intermittently unreliable on real
hardware - switching color, or even a fresh "on" from "off", would
sometimes need more than one attempt, and a failed attempt typically
went dark rather than landing on either color (consistent with the
*second* frame getting lost while the MCU was presumably still busy
with the first - this protocol has no checksum or acknowledgement at
all, so nothing but timing enforces this). A live strace of one
successful real `led power red on` run showed the vendor itself
pausing **~400ms** between the two frames (confirmed via its own
timestamps: last byte of "clear" at `.396806`, first byte of "set" at
`.797133`). A separate, unsuccessful attempt showed the vendor
retrying the "clear" frame alone 7 times over ~2.7s before the
process was killed - plausibly its own retry-on-failure logic for the
same underlying unreliability, though that path wasn't captured
through to a full successful sequence. `wd-control-tool` reproduces
only the confirmed ~400ms pause (`pkg/mcu/ex2.go`'s
`ex2PowerLEDInterFrameDelay`), not a retry loop - the one full
capture available only shows a clean two-frame success with pacing,
never a confirmed multi-attempt success to copy exactly.

**EX4's power LED mechanism is now CONFIRMED** (this spec previously
flagged it unconfirmed, pending an EX4-specific capture - that
capture has now happened). A live strace of the real `led power blue
on` on real EX4 hardware confirmed it does NOT touch `/dev/ttyS1` at
all - only `/dev/mem`, mmapping physical page `0xf1010000`. Decompiling
the real `led` binary (not `libshare.so` - the LED-specific logic
lives in `led` itself, which only calls `libshare.so`'s generic
`LIB_Read_CPU_Register`/`LIB_Write_CPU_Register`) gave the exact
mechanism, cross-checked across all 4 of `blue on`/`blue off`/
`red on`/`red off`'s own decompiled handlers (internally consistent
with each other, not just a single example):

```c
// e.g. "blue on" (power_blue_on, address 0xa3b4):
clear(0x10100, redBit);  clear(0x10108, redBit);   // always clear the OTHER color first
set  (0x10100, blueBit); clear(0x10108, blueBit);  // own color: set in reg1, clear in reg2
// "blue blinking" (shared with several unrelated system states, e.g.
// booting - same mechanism, not a coincidence):
clear(0x10100, redBit);  clear(0x10108, redBit);
set  (0x10100, blueBit); set  (0x10108, blueBit);  // own color SET in BOTH regs for blinking
```

This is **exactly** what `wd-control-tool`'s own `pkg/led.Plan` already
computed for `Power` (addresses `0xF1010100`/`0xF1010108`, `redBit =
1<<28`, `blueBit = 1<<29`, same clear-other/set-own/all-3-states
logic) - the original "confirmed identical to EX2" claim this spec's
EX2 section above shows was WRONG for EX2 turned out to be accidentally
RIGHT for EX4, now confirmed independently rather than by continued
inheritance from that same disproven claim. **Also confirmed on real
EX4 hardware directly** (not just the decompile): `wd-control-tool -b
ex4 -l power red on` lit the real red power LED, and both colors'
`off` and `blinking` states were separately confirmed working too.
EX4's own `led -h` text only documents `power blue on/off/blinking`
(no mention of red at all), but the dispatch code has a full, working
`power red on/off/blinking` branch using the identical mechanism (just
the opposite
bit) - `wd-control-tool` was already right to expose red for EX4 too,
this was just never confirmed until now.

## RTC (EX2/EX4)

Rides the legacy MCU protocol's `RDateAndTime`/`WDateAndTime`
commands - see "EX2/EX4 protocol" above. The vendor `rtc` tool itself
has **no direct `/dev/rtc0` ioctl path** despite that string appearing
in the binary (dead/unused code, or legacy fallback never exercised);
every real read/write goes through `up_send_ctl`. The real tool's own
`-h` (EX2, real hardware):

```
rtc - query and set the hardware clock (RTC)
Functions:
  -r    read RTC time and print it
  -w    read time from system and write to rtc(SYS -> RTC)
  -s    read time from rtc and write to sys(RTC -> SYS)
  (also: -b system booting, -l record log, -c +227s/month drift
  compensation, -d reset to 2014/01/01 00:00:00 - none of these are
  wired up in wd-control-tool)
```

`wd-control-tool -r`/`--rtc` mirrors these three letters directly
(`w`/`r`/`s` - not the full words).

**The RTC stores UTC, not local time** - confirmed on real EX2
hardware by running `date` and the vendor `ex2.sh "rtc -w"` back to
back in one session: `date` printed `Mon  2 Nov 18:14:14 +03 2026`
(the host's own local time) while the vendor tool's own `rtc: system
time: 2026/11/2 Mon 15:14:15` line printed exactly 3 hours earlier -
i.e. the vendor applies NO timezone adjustment at all when it reads
"the current time" to write to the RTC, and (by the same logic)
prints none back when reading it either. `wd-control-tool`'s own
`EX2Writer`/`EX4Writer.Set` convert their input to UTC first
(`pkg/rtc/rtc.go`); `EX2Reader.Read`'s returned `time.Time` is tagged
`time.UTC`, not `time.Local`; and the CLI's own `-w`/`-r`/`-s`
(`cmd/wd-control-tool/main.go`'s `buildRTCWriteAction`/
`buildRTCReadAction`) follow the same convention throughout. This was
missed in the very first version of this feature (every test
happened to construct its own example times already tagged UTC, so
nothing caught it) - found only by comparing `date` against the
vendor tool directly, not by reasoning about it.

- **Write (SYS -> RTC, `rtc -w`)**: confirmed for **both** EX4 (see
  `WDateAndTime` above) and EX2. EX2's own live capture (`rtc -w`
  while the host's system time was 2026-11-02 Mon 13:38:58) resolved
  the "Unresolved" item below: `FA 01 08 02 07 58 38 13 01 02 11 26
  FB` - same 13-byte shape as EX4's (`FA b4 b5 b6 b7 <7 BCD fields>
  FB`), with EX2's own header. **One correction to this spec's own
  table dump**: B7 here is `07`, not the `01` the table recorded (the
  same value as `RDateAndTime`'s own row) - the table extraction was
  wrong for this one field; `RDateAndTime`'s B7=`01` WAS confirmed
  correct by the same capture. Prints `rtc: system time: ...`
  matching the vendor's own wording on success - this had been
  missed entirely at first (a real write ran silently, unlike every
  other rtc subcommand), found via a direct side-by-side with the
  vendor tool. **A `RDateAndTime` read immediately following a
  `WDateAndTime` write can intermittently come back with some
  fields right and others wrong, or with no reply at all** -
  repeated real-hardware testing (`rtc r`+`rtc w`+`rtc s` chained,
  many attempts, several seconds apart, both as separate process
  invocations and chained in one process) showed this reproduces
  every few attempts (roughly 5-10 per 50). **Root cause CONFIRMED**
  (not just suspected), and it is on THIS tool's own side, not the
  MCU's - an early diagnosis blamed a stale RTC-read cache inside
  the MCU firmware, but that was wrong (see below for how it was
  disproven). The real cause: `sendBytes` (`pkg/mcu/serial.go`)
  returns as soon as the last byte of a frame is handed to the
  kernel's write() - at 115200 baud a 13-byte frame still takes
  ~1.1ms to actually finish shifting out over the wire, and `write()`
  returning only means it was queued, not transmitted. `SetDateAndTime`'s
  own verify-read (added as the first fix attempt, see below) calls
  `flushInput()` (`ioctl(TCIFLUSH)`) under 1ms after that write
  returns, to discard anything stale before sending the read request -
  on this UART driver, flushing INPUT that soon after a write can
  also corrupt the tail of that write's STILL-IN-FLIGHT OUTPUT (not
  just POSIX-documented behavior, but confirmed by testing: the MCU
  then never receives a complete, valid command at all, which is why
  failed attempts got back only the unrelated unsolicited `FA 30 00
  00 00 00 FB` noise frame and nothing else - not a stale-but-
  decodable reply, a genuinely incomplete command on the MCU's end).
  **Fixed** in `Port.sendBytes`: it now drains (`ioctl(TCSBRK, 0)`,
  the kernel's own `tcdrain()` equivalent - waits exactly as long as
  transmission actually takes, not a fixed sleep) before returning,
  so by the time any caller's `flushInput()` runs, the write it just
  sent has genuinely, physically finished going out. Confirmed on
  real hardware: multiple full 50-iteration `rtc r`+`rtc w`+`rtc s`
  stress runs with zero corruption after this fix, where the same
  test reliably reproduced the bug every run before it.

  The MCU-firmware-cache theory was disproven by comparing this
  tool's own byte-level trace (`WD_MCU_TRACE`, added specifically
  because wrapping the whole CLI in `strace` made the corruption
  stop reproducing across 3 full 50-iteration runs - almost
  certainly `strace`'s own per-syscall overhead giving the race more
  breathing room than real use ever has, so a no-ptrace in-process
  trace was built instead) against the real vendor `up_send_daemon`/
  `up_read_daemon` (found by tracing `/tmp/msg_info.txt`'s PID,
  confirmed as a second long-lived read daemon by name via `ps`):
  the vendor's own `RtcRetryLog` showed it retries on plain timeouts
  routinely too (not rare), ruling out "the vendor never has this
  problem" as a useful signal either way. Matching the vendor's own
  `open()`/`termios` config exactly (no `O_SYNC`, `IGNPAR` set) was
  tried and had zero effect - a dead end, since by then the real fix
  (draining before flushing) had already been found by direct
  reasoning about the write path's own timing, not by vendor
  comparison. A write-then-read-verify loop
  (`(*mcu.EX2).SetDateAndTime`, `ex2RTCVerifyAttempts`) was added as
  the first mitigation attempt and is kept as defense in depth (it
  now almost never needs to retry at all): it reads straight back
  what it just wrote and re-sends the write if the readback doesn't
  match, up to 3 attempts - this does NOT reintroduce the
  implausible-year sanity check rejected earlier in this
  investigation (that one risked misdiagnosing a genuinely dead RTC
  battery as corruption): it only ever compares against a value this
  same call just wrote, never against an absolute plausibility
  threshold.
- **Read (RTC -> SYS)**: confirmed for **both EX2 and EX4** - a live
  capture of `rtc -r` (RTC itself reading 2026/11/2 Mon 13:39:43 on
  EX2) decoded the reply: `FA 01 08 01 01 43 39 13 01 02 11 26 FB` -
  the request's own 5-byte header echoed back verbatim, followed by
  the same 7-field BCD payload order the write side uses
  (`sec,min,hour,dow,dom,mon,year`), confirmed field-by-field against
  the printed date. This was the single biggest open item in this
  whole spec (see "Unresolved") - now resolved for both boards.
  **EX4's own `RDateAndTime` reply does NOT echo its own request
  header** (unlike EX2): a live capture on real EX4 hardware (RTC
  reading 2026/11/4 20:52:57 UTC) decoded `FA 01 00 03 07 57 52 20 03
  04 11 26 FB` - the request's own b7 is `01`, but the reply's is
  `07` instead (the same byte WDateAndTime's own table uses, not
  RDateAndTime's) - see `mcu.EX4DecodeDateAndTime`'s own doc comment.
  **EX4's `dow` byte uses a DIFFERENT convention than EX2's**,
  confirmed by the SAME capture: the decoded dow was `3`, matching
  exactly what had just been written moments earlier (so NOT a
  storage-side transform), yet the vendor's own `rtc -r` printed
  "Tue" for it - 2026-11-04 is actually a Wednesday (2026-11-01 is a
  Sunday). Only consistent with EX4's firmware expecting
  `1=Sunday..7=Saturday`, not EX2's (also hardware-confirmed)
  `0=Sunday..6=Saturday`. Fixed on the write side
  (`rtc.EX4Writer.Set` now sends `Weekday()+1`); the read side
  (`EX4DecodeDateAndTime`) returns the raw byte as-is, same as EX2's
  decoder - this tool's own printed weekday is always computed from
  the date itself (`time.Date(...).Weekday()`), never from the wire's
  dow byte, so it doesn't inherit the vendor's own mislabeling either
  way. Rapid-fire repeated `RDateAndTime` requests (e.g. several
  `-r r` actions chained with zero think-time in one command) were
  found to get corrupted/stale replies after the first one on EX2 -
  **not an MCU timing issue**: a fixed pre-request pause had zero
  measurable effect, and the real cause was a bug on this tool's own
  side (`Port.recvBytes`, `pkg/mcu/serial.go`, now fixed - see
  BUILDING.md for the full story). `cmd/wd-control-tool/main.go`'s
  `buildRTCReadAction` also retries a decode failure a few times
  (shared by both boards) as defense in depth, since this protocol
  still has no checksum or acknowledgement either way.
- **Sync (RTC -> SYS, also setting the host's system clock, `rtc
  -s`)**: for both EX2 and EX4, this is just the read above plus
  `settimeofday(2)` (`pkg/rtc.SetSystemTime`) - no separate MCU
  capture needed. The clock-setting side effect itself is real and
  hardware/OS-affecting, so (unlike every other confirmed behavior in
  this spec) it is deliberately **not** exercised by this project's
  own automated tests - see BUILDING.md.

## Display (EX4 only)

`SysName` (see "EX2/EX4 protocol", group 9). EX2 and EX2U have no
display at all.

### LCD sleep mode - `LCDSleepMode`, confirmed on real EX4 hardware

Found while auditing the full 82-row EX4 command table for anything
display-related beyond `SysName`: `LCDSleepMode` (b4=`0x04`,
b5=`0x82`, dispatch group 27, row 52 - previously undocumented, like
every other group this spec doesn't describe in "Frame shapes").
Rather than decompile `up_send_ctl`'s group-27 builder, this was
confirmed the simpler way: `up_send_ctl` already knows how to send
this command correctly, so a live strace of the real,
working `up_send_ctl LCDSleepMode 0`/`LCDSleepMode 1` on real EX4
hardware captured the exact wire bytes directly, no decompilation
needed:

| Command | Frame |
|---|---|
| `LCDSleepMode 0` | `FA 04 82 00 00 00 FB` |
| `LCDSleepMode 1` | `FA 04 82 01 00 00 FB` |

This is the exact same "replace the table's B6 slot with a runtime
value" shape `FanControl` (group 24) already uses (`buildValueFrame`)
- group 27 is a different dispatch group number, but not a new frame
shape.

**The value's meaning was confirmed on real hardware, not assumed
from the vendor's own `up_send_ctl LCDSleepMode` (no args) usage
text** (`0: OFF, 1: ON`) - that text alone is ambiguous about WHAT is
off/on (the sleep *feature*, or the screen itself), and a first,
hasty real-hardware report of the opposite mapping turned out to be a
mis-report, corrected on a careful one-command-at-a-time retest:
value `1` makes the physical screen go dark (sleep mode ON), value
`0` lights it back up (sleep mode OFF) - matching the vendor's own
"0: OFF, 1: ON" wording read as describing the sleep feature's own
state, not the screen's. `wd-control-tool`'s own
`mcu.EX4LCDSleepFrame`/`LCDSleep` take an explicit `sleep bool`
rather than a raw value byte specifically to avoid this ambiguity at
every call site; the CLI's own `-s`/`--sleep on|off` maps `on` to
`sleep=true` (screen dark) and `off` to `sleep=false` (screen lit).
**Confirmed working on real EX4 hardware** via `wd-control-tool`
itself (not just the vendor tool).

## Unresolved (tracked here so future work doesn't re-derive it)

1. **The MCU-to-host reply frame format** is mostly still undecoded,
   with three exceptions now: EX2U's `tmp_get` reply (see "EX2U
   protocol" above, `wd-control-tool -b ex2u -t`), EX2's
   `FanStatusGet` reply (see "Fan status read-back" above,
   `wd-control-tool -b ex2 -f rpm`) and EX2's `RDateAndTime` reply
   (see "RTC" above, `wd-control-tool -b ex2 -r r`/`-r s`) - the first
   two reuse the same 7-byte `FA .. FB` shape as every request, with
   the answer in byte 5; `RDateAndTime`'s reply is the 13-byte
   `WDateAndTime` shape instead (request header echoed back, 7 BCD
   fields where the request had its const+FB). Still unknown, and
   **not needed for this tool's current feature set** (so not being
   chased - same as item 5 below): the general request/reply
   correlation mechanism (does every command get a reply, or only
   reads?), EX2/EX4's other sensor reads (`ThermalGet`/
   `GetThemister`), any acknowledgement that a write command was
   received, and the stray trailing byte seen after EX2U's own
   `tmp_get` reply (possibly an unprompted second reply). **Still
   wanted**, unlike the above: EX4's own `RDateAndTime` reply (do not
   assume it matches EX2's - see "RTC" above) - needed before EX4's
   `-r`/`-s` can be implemented, which this tool's user does want
   eventually (EX4's `-w` alone is next up, not blocked on this).
   Closing any of the above needs either a full decompile of
   `up_read_daemon`'s `RS232_Read` parsing loop, or (more reliably,
   per this project's own house rule - proven 4 times now) a live
   UART capture of a real reply.
2. ~~EX2's `WDateAndTime`/`RDateAndTime` write-side frame layout~~ -
   **resolved**: a live `rtc -w`/`rtc -r` capture confirmed both
   (header bytes b4=01,b5=08, b6=01/02 for read/write, b7=01/07
   respectively - see "RTC" above and this section's own table-dump
   footnote for the b7 correction).
3. ~~HD-bay blue LED channel - possible SATA-register involvement~~ -
   **resolved**: a direct register-page capture confirmed hd0/hd1
   blue are plain GPIO bits (25/23) in the same `0xF1018000` page as
   red, not a SATA-controller register at all - see "LED (EX2/EX4)"
   above.
4. ~~EX2's fan level-to-bitmask order~~ - **resolved**, see "Fan
   control" above (was wrong; confirmed and fixed via real-hardware
   RPM read-back).
5. Two anomalous command-table rows that don't fit the normal
   category-byte scheme: EX2's `pwmspeed` (b4=`0x27`), EX4's
   `ErrStatus` (b4=`0x55`). Not investigated (not needed for this
   tool's current feature set). (EX2's third anomalous row,
   `NewPWRLed`, b4=`0x26`, is no longer unresolved - see "LED
   (EX2/EX4)", "Power LED".)
6. EX2U's `sys_reboot` byte sequence was never captured (see EX2U
   protocol table) - **not needed**, this tool's user has confirmed
   this functionality (and others like it) isn't wanted; don't guess
   it if that ever changes.
7. ~~EX2's `RDateAndTime` write-then-read corruption~~ - **resolved**:
   see "RTC" above. The real cause (`Port.sendBytes` returning before
   a write had actually finished transmitting, so a `flushInput()`
   issued shortly after could corrupt the tail of it) is fixed in
   `sendBytes` itself (draining before returning), which protects
   every caller - not just `SetDateAndTime`'s own write-then-verify,
   but also a standalone read by a completely different program
   shortly after this tool wrote the RTC, since the write this tool
   sent will have genuinely finished by the time it returns. A
   formerly-tracked residual gap (no defense for a bare read with no
   preceding write by THIS tool in the same process, e.g. a cold
   `rtc -s` at boot) no longer has a known failure mode to defend
   against, now that the actual cause is understood and fixed at the
   source rather than papered over.
8. ~~EX4's HD-bay LED mechanism (assumed identical to EX2's)~~ -
   **resolved**: it isn't - a real-hardware failure report plus a
   live strace plus decompiling the real `led` binary found EX4 uses
   a completely different register pair for red (per-bay, like EX2,
   but at `0xf1010140`/`0xf1010148` instead of
   `0xF1018140`/`0xF1018150`) and a single register SHARED by all
   four bays for blue (`0xe00104f0` - the vendor's own code reads the
   bay-index argument but never uses it) - see "HD-bay", "EX4's
   HD-bay mechanism is DIFFERENT from EX2's" above. hd2/hd3 are now
   confirmed on EX4 too (they were never confirmed for EX2, which
   doesn't physically have them).
9. ~~EX2's "blinking" has no confirmed frame, for either power or
   HD-bay~~ - **resolved for power (both colors) and HD-bay RED**: a
   live strace confirmed `led power red/blue blinking` - see "Power
   LED" (the MCU frame's own `0x20` flag bit) above. EX2's HD-bay
   `blinking` needed two attempts: a first register dump (reading
   `hdBayReg2` twice during an already-blinking state) looked like it
   confirmed RED but had no actual effect on real hardware; a proper
   follow-up capture - diffing a full register range between a known
   `on` state and the vendor's own `led hd0/hd1 red blinking`, across
   two independent real transitions - found the real control: a THIRD
   register, `hdBayBlinkEn` (`0xF1018148`), one bit per bay - and a
   follow-up fix once `on`/`off` turned out to have no effect after a
   real `blinking` (leaving `hdBayBlinkEn`'s bit set kept the hardware
   blink generator overriding both registers; `on`/`off` now also
   clear it). See "HD-bay" above for the full story. BLUE's own
   HD-bay blinking is confirmed NOT to work (the vendor's own command
   was tested directly on real hardware) and remains an error.

## Author

Symo <symops@gmail.com>
