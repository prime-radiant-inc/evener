package clipboard

import (
	"strings"
	"testing"
)

// FuzzNormalizePastedPath drives the package's real pasted-text parser. The four
// recognised forms (file:// URL, quoted path, Windows/UNC path, POSIX/WSL path)
// all flow through NormalizePastedPath, which then feeds the path conversions
// (ConvertWindowsPathToWSL / FileURIToPath / IsWindowsPath). The functions are
// pure (no FS access), so the oracle is a no-panic floor plus an idempotence
// invariant: a normalized non-empty POSIX-or-windows result must be a fixed
// point of NormalizePastedPath.
func FuzzNormalizePastedPath(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"/home/jesse/a.png",
		"\"/home/me/My Pictures/a.png\"",
		"'/tmp/x.gif'",
		"file:///home/jesse/a.png",
		"file://localhost/home/jesse/a.png",
		`C:\Users\Alice\foo.png`,
		`C:/Users/Alice/foo.png`,
		`\\server\share\img.png`,
		"/mnt/c/Users/Alice/foo.png",
		"~/pics/a.png",
		"./rel.png",
		"../up.png",
		"not a path at all",
		"line1\nline2",
		"file://",
		"file://host",
		"C:",
		"Z:\\",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, text string) {
		got := NormalizePastedPath(text)

		// Exercise the sibling path converters on the same input; all are pure.
		_ = IsImageFile(text)
		_ = IsWindowsPath(text)
		_ = MediaTypeForPath(text)
		_ = FileURIToPath(text)
		_ = ConvertWindowsPathToWSL(text)

		// Idempotence: for a result that is itself a recognised single-path token
		// (POSIX-rooted or a Windows path), re-normalizing must not change it.
		// file:// and quoted inputs are excluded: normalization unwraps them, so
		// they are intentionally not fixed points. Results containing whitespace
		// are also excluded: an unquoted path with spaces is correctly rejected on
		// the second pass (only quoted POSIX paths may contain spaces).
		if got == "" || strings.ContainsAny(got, " \t") {
			return
		}
		if IsWindowsPath(got) || strings.HasPrefix(got, "/") {
			if again := NormalizePastedPath(got); again != got {
				t.Fatalf("NormalizePastedPath not idempotent:\n in=%q\n once=%q\n twice=%q", text, got, again)
			}
		}
	})
}
