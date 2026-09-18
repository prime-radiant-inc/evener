// Package interactiveartifacts defines the bounded public artifact contract.
package interactiveartifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
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
		exact, ok := new(big.Rat).SetString(string(value))
		if !ok {
			return errors.New("invalid JSON number")
		}
		if f == 0 && exact.Sign() != 0 {
			return errors.New("nonzero number underflows binary64")
		}
		if exact.IsInt() && new(big.Int).Abs(exact.Num()).Cmp(big.NewInt(MaxSafeInteger)) > 0 {
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
	return fmt.Sprintf("v1:%s", hex.EncodeToString(sum[:])), nil
}
