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

// unknownSubcommandError reports a subcommand name that a command group
// (`evener openai`, `evener plugin marketplace`, ...) does not have.
type unknownSubcommandError struct {
	group string
	name  string
}

func (e *unknownSubcommandError) Error() string {
	return fmt.Sprintf("unknown %s command %q", e.group, e.name)
}
