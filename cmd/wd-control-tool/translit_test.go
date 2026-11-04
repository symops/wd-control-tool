package main

import "testing"

func TestTransliterateCyrillic(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Привет", "Privet"},
		{"привет", "privet"},
		{"ПРИВЕТ", "PRIVET"}, // only the FIRST Latin letter of each mapping is capitalized, see doc comment
		{"Hello", "Hello"},
		{"", ""},
		{"Щука", "Shchuka"},
		{"щука", "shchuka"},
		{"объём", "obem"}, // ъ/ь drop, ё -> e
		{"123 ABC", "123 ABC"},
		{"Ёлка", "Elka"},
		{"Ж", "Zh"},
		{"ж", "zh"},
		{"日本語", "日本語"}, // outside the 33-letter table entirely - left untouched
	}
	for _, c := range cases {
		if got := transliterateCyrillic(c.in); got != c.want {
			t.Errorf("transliterateCyrillic(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTransliterateCyrillic_AllLettersCovered(t *testing.T) {
	const alphabet = "абвгдежзийклмнопрстуфхцчшщъыьэюя"
	for _, r := range alphabet {
		got := transliterateCyrillic(string(r))
		if got == string(r) {
			t.Errorf("letter %q was not transliterated at all", r)
		}
	}
}

// transliterateCyrillic takes a free-form string straight from
// os.Args (the display text), which is not guaranteed to be valid
// UTF-8 - confirm it never panics regardless of content (invalid
// UTF-8, very long strings, embedded NUL/control bytes, emoji, mixed
// scripts, ...), and that it's idempotent: every lowercase Cyrillic
// letter this function knows about is removed from the output on the
// first pass, so running it again on its own output must be a no-op.
func FuzzTransliterateCyrillic_NeverPanicsAndIsIdempotent(f *testing.F) {
	for _, seed := range []string{
		"Привет", "привет", "ПРИВЕТ", "Hello", "", "щука", "объём",
		"\x00", "\xff\xfe", "🔥", "日本語", "Mixed привет text",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := transliterateCyrillic(s)
		twice := transliterateCyrillic(once)
		if once != twice {
			t.Fatalf("transliterateCyrillic(%q) = %q, but applying it again gives %q - not idempotent", s, once, twice)
		}
	})
}
