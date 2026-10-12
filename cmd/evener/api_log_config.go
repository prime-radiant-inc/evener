package main

import (
	"errors"
	"fmt"
	"strings"
)

// errInvalidAPILog identifies an --api-log value that is neither on nor off.
var errInvalidAPILog = errors.New("invalid --api-log")

// parseAPILog maps the --api-log flag value to the durable API-request logging
// decision. API-request logging is opt-in: empty means off, matching the flag's
// documented default, so a session that did not ask to be recorded keeps its
// ownership file empty instead of persisting every provider request and
// response body.
func parseAPILog(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "off":
		return false, nil
	case "on":
		return true, nil
	default:
		return false, fmt.Errorf("%w %q (want on or off)", errInvalidAPILog, value)
	}
}
