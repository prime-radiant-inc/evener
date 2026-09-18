package dev

// list-build-flags answers one question for the test gate: which of the
// caller's `go test` flags must also be given to the `go list` that enumerates
// the packages to test.
//
// The answer matters because `go list` and `go test` do not see the same tree.
// -tags selects files, and so can select whole packages; -race, -msan and -asan
// each set a build tag of their own (race, msan, asan), so a package whose only
// files sit behind `//go:build race` exists for `go test -race` and not for a
// plain `go list`. The gate enumerates first and hands `go test` the list, so
// anything the enumeration cannot see is not tested, and nothing says so.
//
// A flag whose value is the next argument is consumed with it before anything
// is classified, so that value is never read as a flag: `-run -race` is a regex
// whose text is `-race`, and reading it as a build flag would hand the
// enumeration a sanitiser the caller never asked for. The value-taking flags
// are shardplan.go's tables, which already hold every `go test` flag of that
// shape: this walker is a permissive filter over whatever the gate was handed,
// while the shard runner's parser is exhaustive and refuses a flag it does not
// know, but the two agree on which flags have a value to consume.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// packageSelectionValueFlags take their value as the next argument and change
// which packages exist.
var packageSelectionValueFlags = map[string]bool{"-tags": true}

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

// normalisedFlag is a caller's flag in the one spelling everything here
// compares: go's flag package reads --tags and -tags alike, so a reader that
// knows one of them drops the other.
func normalisedFlag(raw string) (whole, name, value string, inline bool) {
	whole = raw
	if strings.HasPrefix(whole, "--") && len(whole) > 2 {
		whole = whole[1:]
	}
	name, value, inline = strings.Cut(whole, "=")
	return whole, name, value, inline
}

// packageSelectionFlags is the answer, in the spelling `go list` will be given.
func packageSelectionFlags(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		whole, name, _, inline := normalisedFlag(args[i])
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
		_, _ = fmt.Fprintln(stdout, f)
	}
	return 0
}
