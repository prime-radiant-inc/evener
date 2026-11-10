package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

// errUnexpectedArguments identifies positional arguments a subcommand does
// not take.
var errUnexpectedArguments = errors.New("unexpected arguments")

// unexpectedArguments reports the positional arguments fs was left holding.
func unexpectedArguments(fs *flag.FlagSet) error {
	return fmt.Errorf("%w: %s", errUnexpectedArguments, strings.Join(fs.Args(), " "))
}

// requireSingleArg enforces a subcommand's one-positional contract: the
// argument is required, and anything trailing it is rejected loudly rather
// than silently dropped — Go's flag package stops parsing at the first
// positional, so an unguarded command would report success having ignored
// the rest (issue #5479). usage is the command's usage line for the
// missing-argument case, noun names what the single argument is, and
// example shows the options-first form the rejection teaches.
func requireSingleArg(fs *flag.FlagSet, usage, noun, example string) (string, error) {
	if fs.NArg() < 1 {
		return "", errors.New(usage)
	}
	if fs.NArg() > 1 {
		return "", fmt.Errorf("%w %q; put options before the %s, e.g. %s",
			errUnexpectedArguments, fs.Arg(1), noun, example)
	}
	return fs.Arg(0), nil
}

// unknownSubcommandError reports a subcommand name that a command group
// (`evener openai`, `evener plugin marketplace`, ...) does not have.
type unknownSubcommandError struct {
	group string
	name  string
}

func (e *unknownSubcommandError) Error() string {
	return fmt.Sprintf("unknown %s command %q", e.group, e.name)
}
