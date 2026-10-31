# wd-control-tool specification

This document is the hardware reference wd-control-tool implements
against. Every claim below was reverse-engineered from the real
vendor binaries (extracted from each board's own rescue `uRamdisk`)
via disassembly (`objdump`/`readelf`, plus direct byte extraction from
the compiled command tables - see "Extraction method"), not from any
vendor documentation. Confidence varies per section; each one says so
explicitly. Where EX2U's protocol was additionally confirmed on real
hardware (live `strace` capture, end-to-end kernel-driver test), that
is called out - EX2/EX4's protocol below is **not yet** hardware
-confirmed, only statically decoded.

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
  at a time with ~100ms inter-byte pacing (the real hardware's own
  margin, reproduced rather than risk finding out it was load-bearing).
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
| `tmp_get_f`/`tmp_get_c` | `FA 03 08 00 00 00 FB` (same wire command; unit conversion is client-side, and no reply has ever been captured - see "Unresolved") |
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
  **EX4 only** - EX2's `RDateAndTime`/`WDateAndTime` share this same
  dispatch group number (2), but EX2's handler was not independently
  decompiled and its write-side byte layout is **not confirmed** (see
  "Unresolved"). Do not assume it matches EX4's.

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
| WDateAndTime | 01 | 08 | 02 | 01 | 00 | 2 |

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

**Consequence for wd-control-tool**: every command in this tool is
fire-and-forget, with no read-back or acknowledgement. This is a
known, deliberate v1 limitation, not an oversight - see "Unresolved".

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
  the same Kirkwood-class "internal registers" window LED uses at a
  different offset):
  - `0xf1018100` ("reg1"): one-hot bits `0x40`/`0x100`/`0x800`/
    `0x1000`/`0x10000`.
  - `0xf1018140` ("reg2"): one-hot bit `0x1`.
  - Clearing mask confirmed as `0x11940` (reg1) / `0x1` (reg2) -
    exactly the union of the bits above; "stop" clears every fan bit.
  - `fan_control -f N` on EX2 (`N` = 0-6) maps to `0`=Stop + 6 fixed
    RPM steps (~4000-9000rpm). The jump table confirms exactly 6
    non-zero bitmask options exist and each level sets exactly one,
    but **which bitmask belongs to which specific level number was
    not independently confirmed** - wd-control-tool's `pkg/fan/ex2.go`
    uses the bitmasks' disassembly order as an inferred level mapping
    (ascending jump-table case order). The relative ordering (higher
    level = faster) should hold even if a specific level's exact RPM
    is off; verify against real hardware before relying on an exact
    target.
  - EX2 *also* exposes `FanStop`/`FanHalf`/`FanFull` over the legacy
    MCU protocol (see table above) - a separate, apparently-unused-by-
    `fan_control` control surface. wd-control-tool does not currently
    call it.

Both EX2 and EX4 read sensors (`ThermalGet`/`GetThemister`,
`FanStatusGet`) over the MCU/UART, but **this tool cannot parse the
reply** (see "Request/response" above) - sensor reading is not
implemented.

## LED (EX2/EX4)

**Direct `/dev/mem` register pokes against a Marvell-style SoC GPIO
controller, physical base `0xF1000000` - no MCU/UART, no ioctl, no
I2C, no sysfs.** Confirmed via `libshare.so`'s `LIB_Write_CPU_Register`/
`LIB_Write_Mem`, which does the textbook `/dev/mem` pattern: `open64
("/dev/mem", O_RDWR|O_SYNC)`, `mmap64` the containing page, poke the
32-bit word, `munmap`/`close`.

| LED | Data-Out | Blink-Enable | Red bit | Blue bit |
|---|---|---|---|---|
| Power | `0xF1010100` | `0xF1010108` | 28 | 29 |
| HD-bay (bay N, N=0..3) | `0xF1010140` | `0xF1010148` | bit `13+N` | **unconfirmed, see below** |

- `on`: clear the other color's bit in both registers (the two colors
  on one zone are mutually exclusive - one bi-color LED), then set own
  bit in Data-Out, clear own bit in Blink-Enable.
- `off`: clear own bit in both registers (and the other color's, same
  as `on`).
- `blinking`: set own bit in **both** Data-Out and Blink-Enable -
  blinking is a genuine hardware feature of the SoC's GPIO controller
  (confirmed by EX2's own `led --help` text: "blue blinking(controlled
  by hw)"), not a software timer loop.

Confirmed identical mechanism on EX2 and EX4 (same `0xF1000000` base,
same wrapper functions in `libshare.so`); only the HD-bay count
differs (EX2: bay0/bay1 only; EX4: bay0-bay3).

`libalpha_common.so` (also linked by `led`) exports **no**
hardware-access functions at all - its entire ~99-function export
table is xmldb/disk/RAID/volume bookkeeping. The one call `led` makes
into it (`dbSetSocket`) is pure software state-tracking, unrelated to
the physical write.

**Unresolved / caveat**: the HD-bay LED's *blue* channel's dispatch
traced, in at least one call site, into **SATA-controller register
space** (`0xE0000000`-based) rather than this GPIO window - possibly
meaning the blue/activity indicator is tied to a SATA activity
register rather than plain GPIO, or possibly a mis-trace of a shared
helper used for something else at that call site. **Not confirmed on
real hardware.** wd-control-tool implements HD-bay blue with the same
GPIO bit scheme as a best-effort pending that confirmation - treat it
as unverified.

## RTC (EX2/EX4)

Rides the legacy MCU protocol's `RDateAndTime`/`WDateAndTime`
commands - see "EX2/EX4 protocol" above. The vendor `rtc` tool itself
has **no direct `/dev/rtc0` ioctl path** despite that string appearing
in the binary (dead/unused code, or legacy fallback never exercised);
every real read/write goes through `up_send_ctl`.

- **Write (SYS -> RTC, `rtc -w`)**: confirmed for EX4 (see
  `WDateAndTime` above). **Not confirmed for EX2** - EX2 routes both
  read and write through one dispatch group whose write-side byte
  layout was not independently decompiled.
- **Read (RTC -> SYS, `rtc -s` - the direction actually used at boot
  in both EX2 and EX4's vendor `/init`)**: **not implemented at all**,
  on either board - it requires parsing the MCU's reply frame, which
  is the single biggest open item in this whole spec (see
  "Unresolved"). wd-control-tool can currently only *write* a time to
  EX4's RTC; it cannot read one back, and cannot sync system time from
  the RTC the way the vendor's boot sequence did.

## Display (EX4 only)

`SysName` (see "EX2/EX4 protocol", group 9). EX2 and EX2U have no
display at all.

## Unresolved (tracked here so future work doesn't re-derive it)

1. **The MCU-to-host reply frame format** is completely undecoded -
   neither its byte framing nor the request/reply correlation
   mechanism. This blocks: RTC read-back (`rtc -s`'s actual direction),
   temperature/fan-status reads, and any acknowledgement/verification
   that a write command was actually received. Closing this needs
   either a full decompile of `up_read_daemon`'s `RS232_Read` parsing
   loop, or (more reliably, per this project's own house rule - see
   EX2U's protocol history) a live UART capture of a real reply.
2. **EX2's `WDateAndTime`/`RDateAndTime` write-side frame layout** -
   header bytes are known (table dump above) but the handler that
   builds the frame from them was not independently decompiled the
   way EX4's was.
3. **HD-bay blue LED channel** - possible SATA-register involvement,
   not GPIO; see "LED" above.
4. **EX2's fan level-to-bitmask order** - the 6 bitmasks are confirmed
   to exist; which exact level (1-6) maps to which was inferred from
   disassembly order, not independently verified per level.
5. Three anomalous command-table rows that don't fit the normal
   category-byte scheme: EX2's `NewPWRLed` (b4=`0x26`) and `pwmspeed`
   (b4=`0x27`), EX4's `ErrStatus` (b4=`0x55`). Not investigated (not
   needed for this tool's current feature set).
6. EX2U's `sys_reboot` byte sequence was never captured (see EX2U
   protocol table) - do not guess it if ever needed.

## Author

Symo <symops@gmail.com>
