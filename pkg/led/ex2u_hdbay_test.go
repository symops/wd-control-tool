package led

import (
	"os"
	"path/filepath"
	"testing"
)

// Confirmed real LED-class device names (see EX2UHDBaySysfsBase's
// own doc comment, from the user's own working shell commands) -
// golden-value test, not a round-trip, since nothing in this package
// derives these names by formula.
func TestEX2UHDBayLEDName(t *testing.T) {
	cases := []struct {
		zone  Zone
		color Color
		want  string
	}{
		{HD0, Blue, "wdmcex2u:blue:hdd1"},
		{HD0, Red, "wdmcex2u:red:hdd1"},
		{HD1, Blue, "wdmcex2u:blue:hdd2"},
		{HD1, Red, "wdmcex2u:red:hdd2"},
	}
	for _, c := range cases {
		got, err := EX2UHDBayLEDName(c.zone, c.color)
		if err != nil {
			t.Fatalf("EX2UHDBayLEDName(%v, %v): %v", c.zone, c.color, err)
		}
		if got != c.want {
			t.Errorf("EX2UHDBayLEDName(%v, %v) = %q, want %q", c.zone, c.color, got, c.want)
		}
	}
}

// EX2U only has hd0/hd1 in this scheme - no power (that's the
// MCU-driven status LED, a different code path entirely) and no
// hd2/hd3 (EX4-only zones). Must fail loudly, not silently guess a
// name.
func TestEX2UHDBayLEDName_RejectsUnknownZone(t *testing.T) {
	for _, zone := range []Zone{Power, HD2, HD3} {
		if _, err := EX2UHDBayLEDName(zone, Red); err == nil {
			t.Errorf("EX2UHDBayLEDName(%v, Red): want error, got nil", zone)
		}
	}
}

func TestEX2UHDBayLEDName_RejectsUnknownColor(t *testing.T) {
	if _, err := EX2UHDBayLEDName(HD0, Color(99)); err == nil {
		t.Error("EX2UHDBayLEDName(HD0, 99): want error, got nil")
	}
}

// Only On/Off are confirmed (a brightness file write) - no blink
// trigger was given or tested, so Blinking must be rejected at plan
// time rather than guessed at a trigger file this tool has never
// verified, for both zones.
func TestEX2UHDBayPlan_RejectsBlinking(t *testing.T) {
	for _, zone := range []Zone{HD0, HD1} {
		if _, err := EX2UHDBayPlan(zone, Red, Blinking); err == nil {
			t.Errorf("EX2UHDBayPlan(%v, Red, Blinking): want error, got nil", zone)
		}
	}
}

// EX2UHDBayPlan's own bad-zone/color propagation - unreachable
// through the CLI (buildEX2UHDBayLEDAction only ever calls it with
// an already-literal "hd0"/"hd1" zone - see that function's own doc
// comment), but EX2UHDBayPlan is exported, so it must still fail
// correctly for a direct caller.
func TestEX2UHDBayPlan_PropagatesNameLookupError(t *testing.T) {
	if _, err := EX2UHDBayPlan(Power, Red, On); err == nil {
		t.Error("EX2UHDBayPlan(Power, Red, On): want error, got nil")
	}
	if _, err := EX2UHDBayPlan(HD0, Color(99), On); err == nil {
		t.Error("EX2UHDBayPlan(HD0, 99, On): want error, got nil")
	}
}

func TestEX2UHDBayPlan_OnOffSucceed(t *testing.T) {
	for _, state := range []State{On, Off} {
		if _, err := EX2UHDBayPlan(HD0, Blue, state); err != nil {
			t.Errorf("EX2UHDBayPlan(HD0, Blue, %v): %v", state, err)
		}
	}
}

// mustReadFile reads a file and fails the test if it can't, mirroring
// mustRead's own role for register reads elsewhere in this package.
func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(b)
}

// EX2UHDBaySet's real write path, against a temp directory standing
// in for /sys/class/leds (pre-created per real LED-class layout, one
// subdirectory per device, each holding its own "brightness" file -
// matching what a real kernel LED-class driver already provides, not
// something this tool creates). Confirms both the exact device
// subdirectory used and the exact bytes written ("1\n"/"0\n",
// matching the user's own `echo 1 > .../brightness` commands this
// was confirmed from) - not just "no error".
func TestEX2UHDBaySet_WritesCorrectValue(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		zone  Zone
		color Color
		name  string
	}{
		{HD0, Blue, "wdmcex2u:blue:hdd1"},
		{HD0, Red, "wdmcex2u:red:hdd1"},
		{HD1, Blue, "wdmcex2u:blue:hdd2"},
		{HD1, Red, "wdmcex2u:red:hdd2"},
	}
	for _, c := range cases {
		dir := filepath.Join(base, c.name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
		brightness := filepath.Join(dir, "brightness")

		if err := EX2UHDBaySet(base, c.zone, c.color, On); err != nil {
			t.Fatalf("EX2UHDBaySet(%v, %v, On): %v", c.zone, c.color, err)
		}
		if got := mustReadFile(t, brightness); got != "1\n" {
			t.Errorf("%v %v on: brightness = %q, want %q", c.zone, c.color, got, "1\n")
		}

		if err := EX2UHDBaySet(base, c.zone, c.color, Off); err != nil {
			t.Fatalf("EX2UHDBaySet(%v, %v, Off): %v", c.zone, c.color, err)
		}
		if got := mustReadFile(t, brightness); got != "0\n" {
			t.Errorf("%v %v off: brightness = %q, want %q", c.zone, c.color, got, "0\n")
		}
	}
}

// The real I/O error branch IS testable here (unlike /dev/mem's own
// apply() success path elsewhere in this project): no root, no real
// hardware and no physical side effect are involved in writing to a
// temp directory that's missing the expected subdirectory - this is
// exactly the shape a real "LED-class driver not loaded" failure
// would take on actual hardware.
func TestEX2UHDBaySet_PropagatesWriteError(t *testing.T) {
	base := t.TempDir() // deliberately empty - no device subdirectory created
	if err := EX2UHDBaySet(base, HD0, Blue, On); err == nil {
		t.Error("EX2UHDBaySet against a missing device directory: want error, got nil")
	}
}

func TestEX2UHDBaySet_RejectsBlinking(t *testing.T) {
	base := t.TempDir()
	if err := EX2UHDBaySet(base, HD0, Blue, Blinking); err == nil {
		t.Error("EX2UHDBaySet(..., Blinking): want error, got nil")
	}
}
