package board

import (
	"strings"
	"testing"
)

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

// Parse had no fuzz coverage at all - every other Fuzz* target in
// this project fuzzes a function several layers away from raw user
// input, but Parse IS the function that takes -b/--board's value
// completely unvalidated. Property: for any string, Parse never
// panics, and whenever it succeeds the result is a real board (never
// Unknown) whose own String() re-Parses to the identical board. That
// last part is exactly the invariant
// cmd/wd-control-tool/main.go's setBoard relies on when it compares
// two -b/--board occurrences by their resolved board.Board rather
// than their raw strings (see that fix's commit) - an alias like
// "ex2-ultra" must resolve to the same Board as "ex2u", not a
// different one that merely happens to also print as "ex2u". Run
// with `go test -fuzz=FuzzParse_NeverPanicsAndRoundTrips ./pkg/board`
// to fuzz beyond the seed corpus.
func FuzzParse_NeverPanicsAndRoundTrips(f *testing.F) {
	f.Add("")
	f.Add("ex2u")
	f.Add("ex2-ultra")
	f.Add("ex2ultra")
	f.Add("ex2")
	f.Add("ex4")
	f.Add("EX2U")
	f.Add("ex5")
	f.Add("\x00")
	f.Add(strings.Repeat("a", 1000))

	f.Fuzz(func(t *testing.T, s string) {
		b, err := Parse(s)
		if err != nil {
			return // correctly rejected
		}
		if b == Unknown {
			t.Fatalf("Parse(%q) returned Unknown with a nil error", s)
		}
		name := b.String()
		if name == "unknown" {
			t.Fatalf("Parse(%q) = %v, whose own String() is %q", s, b, name)
		}
		b2, err := Parse(name)
		if err != nil {
			t.Fatalf("Parse(%q) = %v, but re-parsing its own String() %q failed: %v", s, b, name, err)
		}
		if b2 != b {
			t.Fatalf("Parse(%q) = %v, but Parse(%q) (that board's own String()) = %v - round-trip through String() changed the board", s, b, name, b2)
		}
	})
}
