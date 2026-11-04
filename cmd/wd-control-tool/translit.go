package main

import "unicode"

// cyrillicTranslit maps each lowercase Russian Cyrillic letter to its
// Latin transliteration - EX4's own front-panel LCD has no Cyrillic
// glyphs in its character ROM at all (confirmed: the vendor's own
// `display`/`sysname` tool shows the exact same garbled output for
// Cyrillic text, on the exact same hardware - this is not a bug in
// this tool's protocol encoding, there is no byte value this tool
// could send instead that the physical LCD would render as a
// Cyrillic letter). transliterateCyrillic converts Cyrillic to
// readable Latin instead, same spirit as a passport transliteration
// table (ъ/ь have no Latin sound and are dropped rather than mapped
// to anything).
var cyrillicTranslit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d",
	'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "i", 'к': "k", 'л': "l", 'м': "m", 'н': "n",
	'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch",
	'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "yu", 'я': "ya",
}

// transliterateCyrillic replaces every Russian Cyrillic letter in s
// with its Latin transliteration, leaving every other character
// (ASCII, punctuation, anything already outside this 33-letter
// alphabet) exactly as-is. Case is preserved per-letter: an uppercase
// Cyrillic letter transliterates to its lowercase mapping with only
// the FIRST resulting Latin letter capitalized (e.g. 'Щ' -> "Shch",
// not "SHCH") - good enough for a short status line on a 16-character
// LCD, not a general-purpose case-folding scheme.
func transliterateCyrillic(s string) string {
	var out []rune
	for _, r := range s {
		lower := unicode.ToLower(r)
		latin, ok := cyrillicTranslit[lower]
		if !ok {
			out = append(out, r)
			continue
		}
		if r != lower && len(latin) > 0 {
			latinRunes := []rune(latin)
			out = append(out, unicode.ToUpper(latinRunes[0]))
			out = append(out, latinRunes[1:]...)
		} else {
			out = append(out, []rune(latin)...)
		}
	}
	return string(out)
}
