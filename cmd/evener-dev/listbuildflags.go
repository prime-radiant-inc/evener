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
// A value is consumed with its flag before anything is classified. `-run -race`
// is a regex whose text is `-race`, and reading it as a build flag would hand
// the enumeration a sanitiser the caller never asked for.

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

// goTestValueFlags are the `go test` flags whose value is the next argument.
// They are here to be skipped over with their values, not to be forwarded: the
// only reason this list exists is that a value must not be read as a flag.
//
// cmd/evener-dev/shardplan.go on #1266 grows the full `go test` flag parser for
// the shard runner; when both land these two tables describe one rule in one
// place. Tracked as its own issue rather than resolved across two open PRs.
var goTestValueFlags = map[string]bool{
	"-bench": true, "-benchtime": true, "-blockprofile": true,
	"-blockprofilerate": true, "-count": true, "-coverprofile": true,
	"-covermode": true, "-coverpkg": true, "-cpu": true, "-cpuprofile": true,
	"-exec": true, "-fuzz": true, "-fuzzminimizetime": true, "-fuzztime": true,
	"-gocoverdir": true, "-list": true, "-memprofile": true,
	"-memprofilerate": true, "-mutexprofile": true,
	"-mutexprofilefraction": true, "-o": true, "-outputdir": true,
	"-p": true, "-parallel": true, "-run": true, "-shuffle": true,
	"-skip": true, "-timeout": true, "-trace": true, "-vet": true,
	// The build flags that take a value, so that theirs is skipped too.
	"-asmflags": true, "-buildmode": true, "-compiler": true,
	"-gccgoflags": true, "-gcflags": true, "-installsuffix": true,
	"-ldflags": true, "-mod": true, "-modfile": true, "-overlay": true,
	"-pgo": true, "-pkgdir": true, "-toolexec": true,
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
		case goTestValueFlags[name] && !inline:
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
