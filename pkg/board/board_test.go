package board

import "testing"

func TestParse_ValidNamesAndAliases(t *testing.T) {
	cases := map[string]Board{
		"ex2u":      EX2U,
		"ex2-ultra": EX2U,
		"ex2ultra":  EX2U,
		"ex2":       EX2,
		"ex4":       EX4,
	}
	for s, want := range cases {
		got, err := Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}
		if got != want {
			t.Errorf("Parse(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestParse_Invalid(t *testing.T) {
	for _, bad := range []string{"", "EX2U", "ex2 ultra", "ex5", "monarch", "duo"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q): want error, got nil", bad)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[Board]string{
		EX2U:      "ex2u",
		EX2:       "ex2",
		EX4:       "ex4",
		Unknown:   "unknown",
		Board(99): "unknown",
		Board(-1): "unknown",
	}
	for b, want := range cases {
		if got := b.String(); got != want {
			t.Errorf("Board(%d).String() = %q, want %q", b, got, want)
		}
	}
}

func TestHasDisplay(t *testing.T) {
	cases := map[Board]bool{EX2U: false, EX2: false, EX4: true, Unknown: false}
	for b, want := range cases {
		if got := b.HasDisplay(); got != want {
			t.Errorf("%v.HasDisplay() = %v, want %v", b, got, want)
		}
	}
}

func TestHasMCU(t *testing.T) {
	cases := map[Board]bool{EX2U: true, EX2: true, EX4: true, Unknown: false, Board(99): false}
	for b, want := range cases {
		if got := b.HasMCU(); got != want {
			t.Errorf("%v.HasMCU() = %v, want %v", b, got, want)
		}
	}
}

// Parse and String should round-trip for every real board - a CLI
// error message built from a parsed board's String() must read back
// the same name the user typed (for the canonical, non-alias forms).
func TestParseStringRoundTrip(t *testing.T) {
	for _, name := range []string{"ex2u", "ex2", "ex4"} {
		b, err := Parse(name)
		if err != nil {
			t.Fatalf("Parse(%q): %v", name, err)
		}
		if got := b.String(); got != name {
			t.Errorf("Parse(%q).String() = %q, want %q", name, got, name)
		}
	}
}
