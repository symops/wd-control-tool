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
| `led_set_on` | `FA 03 06 01 00 00 FB` |
| `led_set_off` | `FA 03 06 00 00 00 FB` |
| `led_blink` | `FA 03 06 02 00 00 FB` |
| `led_breath` | `FA 03 06 04 00 00 FB` |
| `sys_ready` | `FA 03 01 00 00 00 FB` |
| `sys_silent` | `FA 03 03 00 00 00 FB` |
| `sys_shutdown` | `FA 03 03 01 00 00 FB` (confirmed genuine hard power cut, not cosmetic) |
| `tmp_get_f`/`tmp_get_c` | request: `FA 03 08 00 00 00 FB`; reply: `FA 03 08 00 00 <F> FB` where `<F>` is the temperature in whole-degree Fahrenheit - confirmed via two live captures (`wd-control-tool -b ex2u -t` alongside a simultaneous `mcu_ctl tmp_get_c` on real hardware): `0x56`=86 decimal alongside "Temperature: 30 C", and separately `0x54`=84 decimal alongside "Temperature: 28 C". `tmp_get_c`'s F-to-C conversion **truncates** rather than rounds ((F-32)*5/9 as plain C-style integer division): the first capture couldn't distinguish the two (86°F is exactly 30°C either way), but the second does - 84°F rounds to 29 but truncates to 28, and the vendor reported 28. The reply's 8th byte in both captures was a trailing `0xFA`, not yet explained - see `pkg/mcu/ex2u.go`'s `EX2UDecodeTempFahrenheit`/`EX2UDecodeTempCelsius`. |
| `fan_set_0/25/50/75/100` | `FA 02 00 <00/40/80/c0/ff> 00 00 FB` |
| `sys_reboot` | **not captured** - do not guess it |

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
- **EX4**: MCU/UART only (`FanControl`, levels 0-4 = Stop/Low/Medium/
  High/Highest, a direct passthrough of `fan_control -f N`'s own
  argument - confirmed via the vendor binary's own identity lookup
  table).
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
- **`blinking` has no confirmed register behavior at all** - no real
  transition was ever tested for it, and guessing one from the old,
  disproven model would repeat the same mistake. `wd-control-tool`
  reports it as an error (`errHDBayBlinkingUnconfirmed`) instead.
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

`<value>` is a bitmask, confirmed via a live strace capturing all 4
combinations of color x on/off:

| Command | Frame 1 (clear other color) | Frame 2 (set this color) |
|---|---|---|
| `led power blue on` | `FA 26 00 04 00 01 FB` | `FA 26 00 11 00 01 FB` |
| `led power blue off` | `FA 26 00 04 00 01 FB` | `FA 26 00 01 00 01 FB` |
| `led power red on` | `FA 26 00 01 00 01 FB` | `FA 26 00 14 00 01 FB` |
| `led power red off` | `FA 26 00 01 00 01 FB` | `FA 26 00 04 00 01 FB` |

This decodes perfectly into: blue bit = `0x01`, red bit = `0x04`, "on"
flag = `0x10`. Frame 1 always clears the *other* color (value = that
color's bit alone, no on-flag - itself a genuine "turn that color
off"); frame 2 sets *this* color to the requested state (value = this
color's bit, OR'd with the on-flag if turning on). Unlike Power,
HD-bay's two colors turned out to have NO confirmed mutual exclusion
(see "HD-bay" above - a stale version of this sentence used to claim
otherwise, before that capture). No frame for "blinking" has been
captured - `wd-control-tool` reports that as an error rather than
guessing one (see `pkg/mcu/ex2.go`'s `EX2PowerLEDFrames`,
`ErrEX2PowerLEDBlinkingUnconfirmed`).

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

**EX4's power LED mechanism is now unconfirmed, not confirmed as this
spec previously claimed** - that claim rested on the same
never-hardware-tested decompile that was just shown wrong for EX2.
No EX4 hardware has been available to re-test this, so
`wd-control-tool` still uses the `/dev/mem` GPIO path for EX4's power
LED pending an EX4-specific capture - changing it without evidence
would just be guessing in the other direction.

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
(`w`/`r`/`s` - not the full words):

- **Write (SYS -> RTC, `rtc -w`)**: confirmed for **both** EX4 (see
  `WDateAndTime` above) and EX2. EX2's own live capture (`rtc -w`
  while the host's system time was 2026-11-02 Mon 13:38:58) resolved
  the "Unresolved" item below: `FA 01 08 02 07 58 38 13 01 02 11 26
  FB` - same 13-byte shape as EX4's (`FA b4 b5 b6 b7 <7 BCD fields>
  FB`), with EX2's own header. **One correction to this spec's own
  table dump**: B7 here is `07`, not the `01` the table recorded (the
  same value as `RDateAndTime`'s own row) - the table extraction was
  wrong for this one field; `RDateAndTime`'s B7=`01` WAS confirmed
  correct by the same capture.
- **Read (RTC -> SYS)**: confirmed for **EX2 only** - a live capture
  of `rtc -r` (RTC itself reading 2026/11/2 Mon 13:39:43) decoded the
  reply: `FA 01 08 01 01 43 39 13 01 02 11 26 FB` - the request's own
  5-byte header echoed back verbatim, followed by the same 7-field
  BCD payload order the write side uses (`sec,min,hour,dow,dom,mon,
  year`), confirmed field-by-field against the printed date. This was
  the single biggest open item in this whole spec (see "Unresolved")
  - now resolved for EX2. **EX4's own `RDateAndTime` reply has not
  had the same capture** - do not assume it matches EX2's shape (EX2
  and EX4 already disagree on `WDateAndTime`'s own header bytes, so
  there's no reason to assume the reply does carry over either); see
  `pkg/rtc.ErrEX4ReadNotImplemented`. Rapid-fire repeated
  `RDateAndTime` requests (e.g. several `-r r` actions chained with
  zero think-time in one command) were found to get corrupted/stale
  replies after the first one - **not an MCU timing issue**: a fixed
  pre-request pause had zero measurable effect, and the real cause
  was a bug on this tool's own side (`Port.recvBytes`,
  `pkg/mcu/serial.go`, now fixed - see BUILDING.md for the full
  story). `cmd/wd-control-tool/main.go`'s
  `buildRTCReadAction` also retries a decode failure a few times as
  defense in depth, since this protocol still has no checksum or
  acknowledgement either way.
- **Sync (RTC -> SYS, also setting the host's system clock, `rtc
  -s`)**: for EX2, this is just the read above plus
  `settimeofday(2)` (`pkg/rtc.SetSystemTime`) - no separate MCU
  capture needed. The clock-setting side effect itself is real and
  hardware/OS-affecting, so (unlike every other confirmed behavior in
  this spec) it is deliberately **not** exercised by this project's
  own automated tests - see BUILDING.md.

## Display (EX4 only)

`SysName` (see "EX2/EX4 protocol", group 9). EX2 and EX2U have no
display at all.

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
   fields where the request had its const+FB). Still unknown: the
   general request/reply correlation mechanism (does every command
   get a reply, or only reads?), EX2/EX4's other sensor reads
   (`ThermalGet`/`GetThemister`), EX4's own `RDateAndTime` reply (do
   not assume it matches EX2's - see "RTC" above), any
   acknowledgement that a write command was received, and the stray
   trailing byte seen after EX2U's own `tmp_get` reply (possibly an
   unprompted second reply - needs another capture to confirm).
   Closing the rest needs either a full decompile of
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
   protocol table) - do not guess it if ever needed.

## Author

Symo <symops@gmail.com>
