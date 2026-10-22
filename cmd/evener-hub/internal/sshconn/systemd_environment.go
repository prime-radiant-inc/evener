package sshconn

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"primeradiant.com/evener/execsupport/shellquote"
)

// Fetch one byte beyond the limit so the local parser can reject oversized files.
// The runner preserves raw bytes; no remote interpreter or shell evaluation is used.
func systemdEnvironmentFileRemote(path string, optional bool) string {
	missing := "exit 1"
	if optional {
		missing = "exit 0"
	}
	return "file=" + shellquote.RemoteWord(path) + "; if [ ! -e \"$file\" ]; then " + missing + "; fi; [ -f \"$file\" ] || exit 1; head -c 65537 < \"$file\""
}

var systemdEnvironmentAssignment = regexp.MustCompile(`^[ \t\r\f\v]*([A-Za-z_][A-Za-z_0-9]*)[ \t\r\f\v]*=(.*)$`)

// Parse EnvironmentFile syntax without evaluating shell expressions. Return only
// the four identity inputs, never unrelated credentials or file contents in errors.
// Quotes delimit only at the beginning of a value. Consume unrelated multiline
// values as one assignment, but identity values must still resolve to one line.
func parseSystemdEnvironmentFile(text []byte) ([]byte, error) {
	if len(text) > 65536 || !utf8.Valid(text) {
		return nil, errOwnerEvidence
	}
	for _, c := range string(text) {
		if c == 0 || c == 0xfeff || (c >= 0xfdd0 && c <= 0xfdef) || c&0xffff >= 0xfffe {
			return nil, errOwnerEvidence
		}
	}
	env := map[string]string{}
	lines := strings.Split(string(text), "\n")
	for i := 0; i < len(lines); i++ {
		assignment := systemdEnvironmentAssignment.FindStringSubmatch(lines[i])
		if assignment == nil {
			continue // Blank lines, comments and lines without an assignment.
		}
		key, raw := assignment[1], strings.TrimLeft(assignment[2], " \t\r")
		var quote byte
		scan := 1
		if strings.HasPrefix(raw, "'") || strings.HasPrefix(raw, "\"") {
			quote = raw[0]
		}
		for {
			for quote != 0 && scan < len(raw) {
				c := raw[scan]
				scan++
				if c == '\\' && quote == '"' && scan < len(raw) {
					scan++ // An escaped quote does not close the assignment.
				} else if c == quote {
					quote = 0
				}
			}
			continuation := !strings.HasPrefix(raw, "'") && (len(raw)-len(strings.TrimRight(raw, "\\")))%2 != 0
			if !continuation && quote == 0 {
				break
			}
			i++
			if i == len(lines) {
				return nil, errOwnerEvidence
			}
			if continuation {
				raw = raw[:len(raw)-1]
				scan = min(scan, len(raw))
			} else {
				raw += "\n"
			}
			raw += lines[i]
		}
		value, err := systemdEnvironmentValue(raw)
		if err != nil {
			return nil, err
		}
		if slices.Contains(ownerEnvKeys, key) {
			if len(value) > 16384 || strings.ContainsAny(value, "\r\n\x00") {
				return nil, errOwnerEvidence
			}
			env[key] = value
		}
	}
	var entries []string
	for key, value := range env {
		entries = append(entries, key+"="+value+"\x00")
	}
	slices.Sort(entries)
	return []byte(strings.Join(entries, "")), nil
}

func systemdEnvironmentValue(raw string) (string, error) {
	if strings.HasPrefix(raw, "'") {
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 || strings.Trim(raw[end+2:], " \t\r") != "" {
			return "", errOwnerEvidence
		}
		return raw[1 : end+1], nil
	}
	var value strings.Builder
	if strings.HasPrefix(raw, "\"") {
		for i := 1; i < len(raw); i++ {
			switch raw[i] {
			case '"':
				if strings.Trim(raw[i+1:], " \t\r") != "" {
					return "", errOwnerEvidence
				}
				return value.String(), nil
			case '\\':
				i++
				if i == len(raw) {
					return "", errOwnerEvidence
				}
				if !strings.ContainsRune("$`\"\\", rune(raw[i])) {
					value.WriteByte('\\')
				}
				value.WriteByte(raw[i])
			default:
				value.WriteByte(raw[i])
			}
		}
		return "", errOwnerEvidence
	}
	// Keep escaped trailing whitespace, but discard unescaped trailing whitespace.
	white := 0
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case ' ', '\t', '\r':
			continue
		case '\\':
			value.WriteString(raw[white:i])
			i++
			if i == len(raw) {
				return "", errOwnerEvidence
			}
			value.WriteByte(raw[i])
		default:
			value.WriteString(raw[white : i+1])
		}
		white = i + 1
	}
	return value.String(), nil
}
