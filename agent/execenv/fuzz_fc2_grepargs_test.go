package execenv

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzFc2BuildRipgrepArgs drives buildRipgrepArgs — the pure ripgrep
// argument-vector core lifted out of Grep — over adversarial output modes, glob
// filters, patterns, and directories. Oracles (beyond never-panic):
//   - determinism;
//   - the fixed no-heading / no-color prefix is always the first three args;
//   - exactly one output-mode flag is present, at index 3, matching outputMode;
//   - "-i" appears in the option region iff caseInsensitive (at most once);
//   - "-g" appears iff the glob filter is non-blank, followed immediately by it;
//   - the pattern and directory are always the final two args, in that order.
func FuzzFc2BuildRipgrepArgs(f *testing.F) {
	f.Add("content", false, "", "foo", "/root")
	f.Add("files_with_matches", true, "*.go", "bar", "/root/sub")
	f.Add("count", true, "  ", "baz", "rel")
	f.Add("", false, "*.md", "--line-number", "/root") // pattern that looks like a flag

	f.Fuzz(func(t *testing.T, outputMode string, caseInsensitive bool, globFilter, pattern, dir string) {
		args := buildRipgrepArgs(outputMode, caseInsensitive, globFilter, pattern, dir)

		args2 := buildRipgrepArgs(outputMode, caseInsensitive, globFilter, pattern, dir)
		if len(args) != len(args2) {
			t.Fatalf("non-deterministic length: %d vs %d", len(args), len(args2))
		}
		for i := range args {
			if args[i] != args2[i] {
				t.Fatalf("non-deterministic arg[%d]: %q vs %q", i, args[i], args2[i])
			}
		}

		// Minimum shape: 3-arg prefix + 1 mode flag + pattern + dir.
		if len(args) < 6 {
			t.Fatalf("too few args: %v", args)
		}
		wantPrefix := []string{"--no-heading", "--color", "never"}
		for i, w := range wantPrefix {
			if args[i] != w {
				t.Fatalf("prefix[%d]=%q, want %q", i, args[i], w)
			}
		}

		// Exactly one output-mode flag, at index 3, matching outputMode.
		var wantMode string
		switch outputMode {
		case "files_with_matches":
			wantMode = "--files-with-matches"
		case "count":
			wantMode = "--count"
		default:
			wantMode = "--line-number"
		}
		if args[3] != wantMode {
			t.Fatalf("mode flag=%q, want %q for outputMode=%q", args[3], wantMode, outputMode)
		}

		// The pattern and dir are always the last two args.
		if args[len(args)-2] != pattern {
			t.Fatalf("penultimate arg=%q, want pattern %q", args[len(args)-2], pattern)
		}
		if args[len(args)-1] != dir {
			t.Fatalf("final arg=%q, want dir %q", args[len(args)-1], dir)
		}

		// The option region between the mode flag and the trailing pattern+dir
		// holds only the optional -i and -g<glob>.
		mid := args[4 : len(args)-2]
		wantI := caseInsensitive
		wantG := strings.TrimSpace(globFilter) != ""
		iCount, gCount := 0, 0
		for j := 0; j < len(mid); j++ {
			switch mid[j] {
			case "-i":
				iCount++
			case "-g":
				gCount++
				if j+1 >= len(mid) {
					t.Fatalf("-g without a following glob value: %v", mid)
				}
				if mid[j+1] != globFilter {
					t.Fatalf("-g value=%q, want %q", mid[j+1], globFilter)
				}
				j++ // skip the glob value
			}
		}
		if wantI && iCount != 1 {
			t.Fatalf("caseInsensitive but -i count=%d: %v", iCount, mid)
		}
		if !wantI && iCount != 0 {
			t.Fatalf("not caseInsensitive but -i present: %v", mid)
		}
		if wantG && gCount != 1 {
			t.Fatalf("non-blank glob but -g count=%d: %v", gCount, mid)
		}
		if !wantG && gCount != 0 {
			t.Fatalf("blank glob but -g present: %v", mid)
		}
	})
}

// FuzzFc2ResolveGrepDir drives resolveGrepDir — the pure directory-resolution core
// shared by Grep's ripgrep and native-fallback arms. Oracles (beyond never-panic):
//   - determinism;
//   - a blank/whitespace path under an absolute root resolves to the root;
//   - an absolute path is returned verbatim (trimmed);
//   - the result is absolute whenever the root is absolute.
func FuzzFc2ResolveGrepDir(f *testing.F) {
	f.Add("", "/root")
	f.Add("   ", "/root")
	f.Add("sub/dir", "/root")
	f.Add("/abs/path", "/root")
	f.Add("rel", "relroot")

	f.Fuzz(func(t *testing.T, path, rootDir string) {
		got := resolveGrepDir(path, rootDir)
		if got2 := resolveGrepDir(path, rootDir); got != got2 {
			t.Fatalf("non-deterministic: %q vs %q", got, got2)
		}

		trimmed := strings.TrimSpace(path)
		if trimmed == "" && filepath.IsAbs(rootDir) {
			if got != rootDir {
				t.Fatalf("blank path under abs root=%q resolved to %q", rootDir, got)
			}
		}
		if filepath.IsAbs(trimmed) && got != trimmed {
			t.Fatalf("abs path %q resolved to %q, want verbatim", trimmed, got)
		}
		if filepath.IsAbs(rootDir) && !filepath.IsAbs(got) {
			t.Fatalf("abs root=%q but result %q is not absolute", rootDir, got)
		}
	})
}
