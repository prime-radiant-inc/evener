package dev

// list-build-flags answers one question for the test gate: which of the
// caller's `go test` flags must also be given to the `go list` that enumerates
// the packages to test.
//
// The answer matters because `go list` and `go test` do not see the same tree.
// -tags selects files, and so can select whole packages; -race, -msan and -asan
// each set a build tag of their own (race, msan, asan); -overlay can add or
// replace a package's files; -mod, -modfile and -compiler can change which
// module graph and package set resolves. A package that exists for `go test`
// under one of those flags and not for a plain `go list` is never tested, and
// nothing says so, so each one is forwarded with its value.
//
// A flag whose value is the next argument is consumed with it before anything
// is classified, so that value is never read as a flag: `-run -race` is a regex
// whose text is `-race`, and reading it as a build flag would hand the
// enumeration a sanitiser the caller never asked for. Normalisation and the
// value-taking tables are shardplan.go's, so both readers consume values from
// the same spellings, including `-test.run` for `-run`.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// packageSelectionValueFlags take their value as the next argument and change
// which packages or files exist, so they are forwarded to the enumeration.
// -C is deliberately absent: it changes directory before the command runs, and
// the gate has already stood in the module's own directory, so a caller's -C
// cannot be applied to this enumeration. Its value is still consumed, so it is
// not misread as a flag.
var packageSelectionValueFlags = map[string]bool{
	"-tags": true, "-overlay": true, "-mod": true, "-modfile": true, "-compiler": true,
}

// packageSelectionBareFlags stand alone and change which packages exist, each
// by setting a build tag of its own name.
var packageSelectionBareFlags = map[string]bool{"-race": true, "-msan": true, "-asan": true}

// consumesValue reports whether name's value is the next argument, and so must
// be skipped over rather than read as a flag. -tags is a selection flag this
// walker forwards, not a skip, and -args terminates the flags; both are handled
// by the walker itself.
func consumesValue(name string) bool {
	if name == "-tags" || name == "-args" {
		return false
	}
	return buildValueFlags[name] || testForwardValueFlags[name] || testRefusedValueFlags[name]
}

// packageSelectionFlags is the answer, in the spelling `go list` will be given.
func packageSelectionFlags(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		whole := goFlag(args[i])
		name, _, inline := strings.Cut(whole, "=")
		if name == "-args" {
			// Everything after -args belongs to the test binary, not to `go
			// test`: a word spelled -race there is an argument whose text is
			// -race, and enumerating under it would build a tree nobody asked
			// for.
			return out
		}
		switch {
		case packageSelectionValueFlags[name]:
			if inline {
				out = append(out, whole)
				continue
			}
			if i+1 < len(args) {
				i++
				out = append(out, name, args[i])
			}
		case packageSelectionBareFlags[name]:
			out = append(out, whole)
		case consumesValue(name) && !inline:
			// Its value is a value, whatever it looks like.
			i++
		}
	}
	return out
}

func listBuildFlagsMain(args []string) int {
	return listBuildFlags(args, os.Stdout, os.Stderr)
}

// listBuildFlags prints one flag per line, which is how a shell can read them
// into an array without splitting a value on its spaces.
func listBuildFlags(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list-build-flags", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev list-build-flags -- <go test flags...>\n\n"+
			"Prints, one per line, the flags that must also be given to the `go list`\n"+
			"that enumerates the packages `go test` will be handed.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	for _, f := range packageSelectionFlags(fs.Args()) {
		// A short write would hand the gate a truncated flag list, and it would
		// enumerate under flags the caller never set. Fail loudly instead, so
		// the gate's own guard stops the run.
		if _, err := fmt.Fprintln(stdout, f); err != nil {
			_, _ = fmt.Fprintf(stderr, "evener-dev list-build-flags: writing flags: %v\n", err)
			return 1
		}
	}
	return 0
}
