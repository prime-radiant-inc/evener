// Package requestutil contains request-building helpers shared by provider
// protocols.
package requestutil

import "encoding/json"

// PositivePointerInt returns the pointed-to value when it is positive.
func PositivePointerInt(value *int) int {
	if value != nil && *value > 0 {
		return *value
	}
	return 0
}

// MinPositiveInt returns the smallest positive value, or zero when none are
// positive.
func MinPositiveInt(values ...int) int {
	best := 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if best == 0 || value < best {
			best = value
		}
	}
	return best
}

// PositiveInt converts the numeric forms produced by request-option decoding
// to an int when the value is positive.
func PositiveInt(value any) int {
	switch value := value.(type) {
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 {
			return int(value)
		}
	case json.Number:
		parsed, err := value.Int64()
		if err == nil && parsed > 0 {
			return int(parsed)
		}
	}
	return 0
}
