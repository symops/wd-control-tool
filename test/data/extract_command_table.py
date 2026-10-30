#!/usr/bin/env python3
"""Dump up_send_ctl's compiled command table byte-for-byte.

Produced ex2_commands.txt / ex4_commands.txt in this directory, which
SPEC.md's "EX2/EX4 protocol" section is built from. Re-run this if you
re-extract the vendor uRamdisks and want to re-verify or extend the
table (e.g. to decode one of the rows this tool doesn't wire up yet).

Usage:
    python3 -I extract_command_table.py <path-to-up_send_ctl>

The vaddr/off/size numbers below are NOT universal constants - they
are each binary's own .data/.rodata section layout (`readelf -S
<binary>`) and the command table's own virtual address (found by
disassembling up_send_ctl's name-dispatch loop and locating the table
it indexes - see SPEC.md, "Extraction method"). EX2 and EX4 happen to
need different numbers because they're different compiled binaries;
if you point this at some other board's up_send_ctl, re-derive them
the same way rather than assuming these apply.
"""
import struct
import sys

# (data_vaddr, data_off, rodata_vaddr, rodata_off, rodata_size, table_vaddr, n_entries)
KNOWN = {
    "ex4": (0x16094, 0x006094, 0xC528, 0x004528, 0x17B0, 0x1609C, 82),
    "ex2": (0x22368, 0x002368, 0x11984, 0x001984, 0x79C, 0x22370, 56),
}


def read_cstr(data, off):
    end = data.index(b"\x00", off)
    return data[off:end].decode("latin1")


def dump(path, data_vaddr, data_off, rodata_vaddr, rodata_off, rodata_size, table_vaddr, n_entries, label):
    with open(path, "rb") as f:
        data = f.read()
    table_off = table_vaddr - data_vaddr + data_off
    print(f"=== {label}: table file-offset=0x{table_off:x}, {n_entries} entries ===")
    stride = 40
    for i in range(n_entries):
        base = table_off + i * stride
        words = struct.unpack_from("<10I", data, base)
        name_vaddr = words[0]
        if name_vaddr == 0:
            name = "<null>"
        else:
            name_off = name_vaddr - rodata_vaddr + rodata_off
            if 0 <= name_off < len(data):
                try:
                    name = read_cstr(data, name_off)
                except Exception as e:
                    name = f"<bad:{e}>"
            else:
                name = f"<out-of-range 0x{name_vaddr:x}>"
        w1 = words[1]
        b4, b5, b6, b7 = w1 & 0xFF, (w1 >> 8) & 0xFF, (w1 >> 16) & 0xFF, (w1 >> 24) & 0xFF
        w2 = words[2]
        w3 = words[3]
        rest = words[4:10]
        print(
            f"[{i:3d}] {name:28s} b4={b4:02x} b5={b5:02x} b6={b6:02x} b7={b7:02x} "
            f"word2=0x{w2:08x} grp(word3)={w3:3d} rest={[hex(x) for x in rest]}"
        )


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <path-to-up_send_ctl-for-ex2-or-ex4>", file=sys.stderr)
        raise SystemExit(1)
    path = sys.argv[1]
    label = "ex4" if "ex4" in path else "ex2" if "ex2" in path else None
    if label is None:
        print("could not tell ex2 from ex4 from the path; rename it to contain 'ex2' or 'ex4'", file=sys.stderr)
        raise SystemExit(1)
    dump(path, *KNOWN[label], label.upper())
