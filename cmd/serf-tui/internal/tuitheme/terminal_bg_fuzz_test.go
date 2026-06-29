package tuitheme

import (
	"math"
	"testing"

	"github.com/muesli/termenv"
)

// FuzzColorParsing drives the package's real color-decode seam: colorToHex
// canonicalizes a termenv RGB color string ("#rgb"/"#rrggbb") and
// relativeLuminanceHex parses the resulting hex via strconv.ParseInt to compute
// WCAG luminance (the basis for system dark/light detection). The parse surface
// is thin (this package is mostly static theme tables), so this targets the only
// real decoders. Oracle: no-panic floor plus a finiteness invariant — luminance
// must never be NaN/Inf (a non-finite value would corrupt the <0.5 dark/light
// comparison). Note: the documented [0,1] range is NOT asserted here because it
// is not enforced for malformed hex — strconv.ParseInt accepts a signed channel
// like "-1" (e.g. input "#00-100"), and colorToHex passes through any 7-char
// '#'-prefixed string without validating its digits. Real callers only ever feed
// a terminal's valid hex, so this stays latent (see report).
func FuzzColorParsing(f *testing.F) {
	seeds := []string{
		"#000000", "#ffffff", "#abc", "#6b9ec8", "#fff",
		"", "#", "#zz", "#12", "#1234567", "not-a-color",
		"#0f0f11", "#GGGGGG", "rgb(1,2,3)", "#00000g",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		// colorToHex over the RGBColor branch (the string-typed decode path).
		hex := colorToHex(termenv.RGBColor(raw))
		if hex != "" {
			if len(hex) != 7 || hex[0] != '#' {
				t.Fatalf("colorToHex returned non-canonical %q for input %q", hex, raw)
			}
		}

		lum := relativeLuminanceHex(raw)
		if math.IsNaN(lum) || math.IsInf(lum, 0) {
			t.Fatalf("relativeLuminanceHex(%q) = %v is non-finite", raw, lum)
		}
		// Canonicalized hex must also yield a finite luminance.
		if hex != "" {
			lum2 := relativeLuminanceHex(hex)
			if math.IsNaN(lum2) || math.IsInf(lum2, 0) {
				t.Fatalf("relativeLuminanceHex(canonical %q) = %v is non-finite", hex, lum2)
			}
		}
	})
}
