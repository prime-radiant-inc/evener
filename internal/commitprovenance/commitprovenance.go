// Package commitprovenance owns the commit-provenance mode and the exact
// Co-authored-by trailer the model is told to write.
package commitprovenance

import (
	"fmt"
	"strings"
	"unicode"
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

// Validate accepts an empty mode (meaning "no override") and either known
// value.
func Validate(mode string) error {
	_, err := Normalize(mode)
	return err
}

// Trailer returns the exact trailer line. It rejects missing fields and
// characters that would break the trailer's name or address syntax instead of
// inventing or rewriting a value.
func Trailer(version, model, host, session string) (string, error) {
	for name, value := range map[string]string{
		"version": version,
		"model":   model,
		"host":    host,
		"session": session,
	} {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("commit provenance: %s is empty", name)
		}
		if hasControl(value) || strings.ContainsAny(value, "<>") {
			return "", fmt.Errorf("commit provenance: %s contains an unsafe character", name)
		}
	}
	if !validAddressPart(host) {
		return "", fmt.Errorf("commit provenance: host %q is not usable in an address", host)
	}
	if !validAddressPart(session) {
		return "", fmt.Errorf("commit provenance: session %q is not usable in an address", session)
	}
	return fmt.Sprintf("Co-authored-by: Evener %s (%s) <%s@%s>", version, model, session, host), nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// validAddressPart keeps the session and host to the characters an ordinary
// address can carry. A dot, hyphen and underscore are accepted; whitespace and
// address delimiters are not.
func validAddressPart(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
