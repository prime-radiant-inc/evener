// Package commitprovenance owns the commit-provenance mode and the exact
// Co-authored-by trailer the model is told to write.
package commitprovenance

import (
	"fmt"
	"strings"
	"unicode"

	"primeradiant.com/evener/appwire"
)

const (
	// ModeCoauthor writes the trailer. It is the default when the mode is
	// empty.
	ModeCoauthor = "coauthor"
	// ModeOff omits the trailer instruction.
	ModeOff = "off"
)

// Normalize maps an empty mode to ModeCoauthor and validates the result.
func Normalize(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case "":
		return ModeCoauthor, nil
	case ModeCoauthor:
		return ModeCoauthor, nil
	case ModeOff:
		return ModeOff, nil
	default:
		return "", fmt.Errorf("commit provenance: unknown mode %q", mode)
	}
}

// Trailer returns the exact trailer line. It rejects missing fields and
// characters that would break the trailer's name or address syntax instead of
// inventing or rewriting a value.
func Trailer(version, model, host, session string) (string, error) {
	for _, field := range [...]struct{ name, value string }{
		{"version", version},
		{"model", model},
	} {
		if strings.TrimSpace(field.value) == "" {
			return "", fmt.Errorf("commit provenance: %s is empty", field.name)
		}
		if strings.ContainsFunc(field.value, unicode.IsControl) || strings.ContainsAny(field.value, "<>") {
			return "", fmt.Errorf("commit provenance: %s contains an unsafe character", field.name)
		}
	}
	if !appwire.ValidRefPart(host) {
		return "", fmt.Errorf("commit provenance: host %q is not usable in an address", host)
	}
	if !appwire.ValidRefPart(session) {
		return "", fmt.Errorf("commit provenance: session %q is not usable in an address", session)
	}
	return fmt.Sprintf("Co-authored-by: Evener %s (%s) <%s@%s>", version, model, session, host), nil
}
