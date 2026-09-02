// Package requestutil contains request-building helpers shared by provider
// protocols.
package requestutil

import (
	"encoding/json"
	"strconv"
)

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

// ReconcileOutputField limits a numeric request-body field to the smallest
// positive wire value, admitted allocation, or provider output cap.
func ReconcileOutputField(body map[string]any, field string, admitted, outputCap *int) {
	if ceiling := MinPositiveInt(PositiveInt(body[field]), PositivePointerInt(admitted), PositivePointerInt(outputCap)); ceiling > 0 {
		body[field] = ceiling
	}
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
		return positiveInt64(value)
	case float64:
		parsed, err := strconv.Atoi(strconv.FormatFloat(value, 'f', -1, 64))
		if err == nil && parsed > 0 {
			return parsed
		}
	case json.Number:
		parsed, err := value.Int64()
		if err == nil {
			return positiveInt64(parsed)
		}
	}
	return 0
}

func positiveInt64(value int64) int {
	converted := int(value)
	if value > 0 && int64(converted) == value {
		return converted
	}
	return 0
}
