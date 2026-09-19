// Package interactiveartifacts defines the bounded public artifact contract.
package interactiveartifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxSourceBytes  = 1 << 20
	MaxStateBytes   = 256 << 10
	MaxRequestBytes = 2 << 20
	MaxJSONDepth    = 64
	MaxSafeInteger  = int64(9007199254740991)
)

// ParseJSON preserves numeric tokens and refuses duplicate decoded object keys.
// maxBytes bounds input work; a container consumes one of the 64 nesting levels.
func ParseJSON(data []byte, maxBytes int) (any, error) {
	if len(data) > maxBytes {
		return nil, errors.New("JSON exceeds byte limit")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("JSON is not UTF-8")
	}
	if err := validateStringEscapes(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := parseValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("JSON has trailing data")
	}
	return value, nil
}

func parseValue(dec *json.Decoder, depth int) (any, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, errors.New("invalid JSON")
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	if depth >= MaxJSONDepth {
		return nil, errors.New("JSON exceeds nesting limit")
	}
	switch delim {
	case '{':
		value := make(map[string]any)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return nil, errors.New("invalid JSON object")
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("invalid JSON object key")
			}
			if _, exists := value[key]; exists {
				return nil, errors.New("duplicate JSON object key")
			}
			child, err := parseValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			value[key] = child
		}
		if end, err := dec.Token(); err != nil || end != json.Delim('}') {
			return nil, errors.New("invalid JSON object")
		}
		return value, nil
	case '[':
		value := make([]any, 0)
		for dec.More() {
			child, err := parseValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			value = append(value, child)
		}
		if end, err := dec.Token(); err != nil || end != json.Delim(']') {
			return nil, errors.New("invalid JSON array")
		}
		return value, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

// CanonicalJSON sorts object keys, preserves array order and json.Number tokens.
func CanonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }

// ValidateState returns canonical object JSON without rewriting numeric tokens.
func ValidateState(data []byte) (json.RawMessage, error) {
	value, err := ParseJSON(data, MaxStateBytes)
	if err != nil {
		return nil, err
	}
	return canonicalState(value)
}

func canonicalState(value any) (json.RawMessage, error) {
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("state must be a JSON object")
	}
	if err := validateNumbers(value); err != nil {
		return nil, err
	}
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	if len(canonical) > MaxStateBytes {
		return nil, errors.New("state exceeds byte limit")
	}
	return canonical, nil
}

func validateNumbers(value any) error {
	switch value := value.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(value), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return errors.New("number is outside finite binary64 domain")
		}
		integer, isInteger, safe := exactSafeInteger(string(value))
		if f == 0 && (!isInteger || !safe || integer != 0) {
			return errors.New("nonzero number underflows binary64")
		}
		if isInteger && !safe {
			return errors.New("integer is outside safe binary64 range")
		}
		if math.Trunc(f) == f && math.Abs(f) > float64(MaxSafeInteger) {
			return errors.New("number rounds outside safe integer range")
		}
	case map[string]any:
		for _, child := range value {
			if err := validateNumbers(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateNumbers(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// Fingerprint v1 includes trusted namespace and submitted semantic arguments.
// Authentication identity belongs in ReceiptKey, never in this digest. Defaults
// are not applied: absence, null and raw numeric spelling remain significant.
func Fingerprint(namespace string, data []byte) (string, error) {
	value, err := ParseJSON(data, MaxRequestBytes)
	if err != nil {
		return "", err
	}
	args, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("arguments must be a JSON object")
	}
	delete(args, "mutationId")
	canonical, err := CanonicalJSON(map[string]any{"namespace": namespace, "arguments": args})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "v1:" + hex.EncodeToString(sum[:]), nil
}

// exactSafeInteger checks decimal spelling using work bounded by token length.
// It never constructs a power of ten from an attacker-controlled exponent.
// Input must already be a JSON number token.
func exactSafeInteger(token string) (value int64, integer, safe bool) {
	negative := strings.HasPrefix(token, "-")
	mantissa := strings.TrimPrefix(token, "-")
	exponent := 0
	if i := strings.IndexAny(mantissa, "eE"); i >= 0 {
		expToken := mantissa[i+1:]
		mantissa = mantissa[:i]
		parsed, err := strconv.ParseInt(expToken, 10, 64)
		bound := int64(len(token) + 32)
		if err != nil || parsed > bound || parsed < -bound {
			parsed = bound
			if strings.HasPrefix(expToken, "-") {
				parsed = -bound
			}
		}
		exponent = int(parsed)
	}
	fractionDigits := 0
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		fractionDigits = len(mantissa) - i - 1
		mantissa = mantissa[:i] + mantissa[i+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return 0, true, true
	}
	trimmed := strings.TrimRight(digits, "0")
	power := exponent - fractionDigits + len(digits) - len(trimmed)
	if power < 0 {
		return 0, false, false
	}
	if len(trimmed)+power > 16 {
		return 0, true, false
	}
	digits = trimmed + strings.Repeat("0", power)
	if len(digits) == 16 && digits > "9007199254740991" {
		return 0, true, false
	}
	value, _ = strconv.ParseInt(digits, 10, 64)
	if negative {
		value = -value
	}
	return value, true, true
}

// validateStringEscapes refuses UTF-16 surrogates that cannot become UTF-8.
// The standard decoder would silently replace these with U+FFFD, losing source,
// state and key identity. Escaped backslashes never begin Unicode escapes.
func validateStringEscapes(data []byte) error {
	inString := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return errors.New("invalid JSON string escape")
		}
		if data[i] != 'u' {
			continue
		}
		code, err := unicodeEscape(data, i)
		if err != nil {
			return err
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return errors.New("unpaired JSON Unicode surrogate")
		}
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+2 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return errors.New("unpaired JSON Unicode surrogate")
		}
		low, err := unicodeEscape(data, i+2)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return errors.New("unpaired JSON Unicode surrogate")
		}
		i += 6
	}
	return nil
}

func unicodeEscape(data []byte, index int) (uint64, error) {
	if index+4 >= len(data) {
		return 0, errors.New("invalid JSON Unicode escape")
	}
	code, err := strconv.ParseUint(string(data[index+1:index+5]), 16, 16)
	if err != nil {
		return 0, errors.New("invalid JSON Unicode escape")
	}
	return code, nil
}
