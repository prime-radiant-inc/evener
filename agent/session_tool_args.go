package agent

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Decoders for model-supplied tool arguments, shared by every session tool.
// This file imports nothing from the tool files and holds no session state.
// The optional*Arg helpers are strict: a present value of the wrong shape is
// rejected as invalid_request. stringArg and optionalIntArg are lenient: a
// value of the wrong shape reads as absent.

// stringArg returns args[key] as a string, or "" when absent or not a string.
func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// optionalIntArg extracts an optional integer pointer from tool arguments.
func optionalIntArg(args map[string]any, key string) *int {
	v, ok := args[key]
	if !ok {
		return nil
	}
	if n, ok := v.(float64); ok {
		ni := int(n)
		return &ni
	}
	return nil
}

// optionalStringArg extracts an optional string argument, rejecting a
// present-but-wrong-type value rather than silently ignoring it.
func optionalStringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("invalid_request: %s must be a string, got %T", key, v)
	}
	return s, nil
}

// optionalBoolArg extracts an optional boolean argument, rejecting a
// present-but-wrong-type value rather than silently ignoring it.
func optionalBoolArg(args map[string]any, key string) (*bool, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("invalid_request: %s must be a boolean, got %T", key, v)
	}
	return &b, nil
}

// optionalWholeIntArg extracts an optional non-negative integer argument,
// rejecting a fractional, negative, out-of-range, or wrong-typed value with
// invalid_request rather than silently reshaping or ignoring it.
func optionalWholeIntArg(args map[string]any, key string) (*int, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, nil
	}
	n, ok := v.(float64)
	if !ok {
		return nil, fmt.Errorf("invalid_request: %s must be a whole number, got %T", key, v)
	}
	if n != math.Trunc(n) || n < 0 || n >= math.MaxInt {
		return nil, fmt.Errorf("invalid_request: %s must be a non-negative whole number, got %v", key, n)
	}
	i := int(n)
	return &i, nil
}

// optionalTimeArg extracts an optional RFC3339 timestamp from tool arguments.
func optionalTimeArg(args map[string]any, key string) (*time.Time, error) {
	raw, err := optionalStringArg(args, key)
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// RFC3339Nano also accepts a plain RFC3339 timestamp, so this admits both
	// whole-second and fractional-second bounds.
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, fmt.Errorf("invalid_request: %s must be an RFC3339 timestamp: %w", key, err)
	}
	return &t, nil
}
