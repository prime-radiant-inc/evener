package main

import (
	"fmt"
	"strings"
)

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
		return false, fmt.Errorf("invalid --api-log %q (want on or off)", value)
	}
}
