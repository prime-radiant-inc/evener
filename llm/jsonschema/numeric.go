package jsonschema

import (
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/llm/jsonschema/decimal"
)

// numberValue deliberately preserves the validator's admitted Go-kind switch.
// fmt.Sprint preserves the existing decimal interpretation of finite Go floats.
// In particular, named numbers, int16, uint16 and uintptr are not admitted.
func numberValue(v any) (decimal.Number, bool) {
	switch v.(type) {
	case json.Number, float32, float64, int, int8, int32, int64, uint, uint8, uint32, uint64:
		n, err := decimal.Parse(fmt.Sprint(v))
		return n, err == nil
	}
	return decimal.Number{}, false
}

// loadNumber compiles a present bound into an immutable numeric identity.
func loadNumber(v any) (*decimal.Number, error) {
	n, ok := numberValue(v)
	if !ok {
		return nil, fmt.Errorf("jsonschema: invalid numeric bound %v", v)
	}
	return &n, nil
}
