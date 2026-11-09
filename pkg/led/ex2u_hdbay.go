package led

import (
	"fmt"
	"os"
	"path/filepath"
)

// EX2UHDBaySysfsBase is the real Linux LED-class directory EX2U's
// own hd0/hd1 bay LEDs live under - CONFIRMED on real EX2U hardware
// (the user's own working shell commands, not a guess):
//
//	echo 1 > /sys/class/leds/wdmcex2u:blue:hdd1/brightness
//	echo 1 > /sys/class/leds/wdmcex2u:blue:hdd2/brightness
//	echo 1 > /sys/class/leds/wdmcex2u:red:hdd1/brightness
//	echo 1 > /sys/class/leds/wdmcex2u:red:hdd2/brightness
//
// (and the identical four paths with "echo 0" for off). Unlike every
// other LED this tool drives - MCU/UART for EX2U's own status LED
// and EX2's power LED; direct /dev/mem GPIO pokes for EX2/EX4's
// hd-bay and EX4's power LED - these are plain Linux LED-class
// devices: a kernel driver already exposes them, so there is no
// protocol or register to reverse-engineer here, only the right
// sysfs paths. Only on/off is confirmed - no blink trigger was given
// or tested, so blinking is reported as an error rather than assumed
// (see EX2UHDBaySet).
const EX2UHDBaySysfsBase = "/sys/class/leds"

// ex2uHDBayLEDNames: the CONFIRMED real LED-class device names (see
// EX2UHDBaySysfsBase's own doc comment) - "hdd1"/"hdd2" (1-indexed,
// the device's own naming), not this tool's 0-indexed hd0/hd1 zone
// naming, which is why this is a lookup table rather than a formula.
// EX2U only has hd0/hd1 - no power/hd2/hd3 in this scheme.
var ex2uHDBayLEDNames = map[Zone]map[Color]string{
	HD0: {Blue: "wdmcex2u:blue:hdd1", Red: "wdmcex2u:red:hdd1"},
	HD1: {Blue: "wdmcex2u:blue:hdd2", Red: "wdmcex2u:red:hdd2"},
}

// EX2UHDBayLEDName looks up zone/color's real LED-class device name.
func EX2UHDBayLEDName(zone Zone, color Color) (string, error) {
	byColor, ok := ex2uHDBayLEDNames[zone]
	if !ok {
		return "", errBadZone(zone)
	}
	name, ok := byColor[color]
	if !ok {
		return "", errBadColor(color)
	}
	return name, nil
}

// EX2UHDBayPlan validates zone/color/state together and returns the
// confirmed LED-class device name (see EX2UHDBaySysfsBase) - the
// pure half of EX2UHDBaySet, used directly by the CLI's -n/--dry-run
// and -D/--debug (and by EX2UHDBaySet itself). Only On/Off are
// confirmed; Blinking is rejected here, at plan time, rather than
// guessed at a "trigger" file this tool has never verified exists or
// behaves as expected - matching every other unconfirmed-state case
// in this package (see e.g. Plan's own HD-bay Blinking case).
func EX2UHDBayPlan(zone Zone, color Color, state State) (name string, err error) {
	name, err = EX2UHDBayLEDName(zone, color)
	if err != nil {
		return "", err
	}
	if state != On && state != Off {
		return "", errEX2UHDBayBlinkingNotSupported(zone, color)
	}
	return name, nil
}

// EX2UHDBaySet writes zone/color's LED-class brightness file to turn
// it on or off, under base (EX2UHDBaySysfsBase on real hardware;
// overridable so tests can point it at a temp directory standing in
// for /sys/class/leds).
func EX2UHDBaySet(base string, zone Zone, color Color, state State) error {
	name, err := EX2UHDBayPlan(zone, color, state)
	if err != nil {
		return err
	}
	value := "0\n"
	if state == On {
		value = "1\n"
	}
	path := filepath.Join(base, name, "brightness")
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		return fmt.Errorf("led: write %s: %w", path, err)
	}
	return nil
}
