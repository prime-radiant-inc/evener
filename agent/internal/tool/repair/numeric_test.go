package repair

import (
	"encoding/json"
	"testing"
)

func TestNumericExact(t *testing.T) {
	for _, pair := range [][2]string{{"9007199254740993", "9.007199254740993e15"}, {"-9007199254740993", "-9007199254740993.0"}, {"18446744073709551617", "18446744073709551617.0"}, {"1e-400", "10e-401"}} {
		if !jsonEqual(json.Number(pair[0]), json.Number(pair[1])) {
			t.Errorf("equivalent numbers not equal: %v", pair)
		}
	}
	for _, pair := range [][2]string{{"9007199254740993", "9007199254740992"}, {"1.0000000000000001", "1"}, {"1e-400", "0"}, {"1e-1000000000", "0"}} {
		if jsonEqual(json.Number(pair[0]), json.Number(pair[1])) {
			t.Errorf("distinct numbers equal: %v", pair)
		}
	}
	for _, tc := range []struct {
		number, kind string
		want         bool
	}{
		{"9007199254740993.0", "integer", true}, {"9.007199254740993e15", "integer", true}, {"1.0000000000000001", "integer", false}, {"1e-400", "integer", false}, {"1e-1000000000", "number", true}, {"0e-1000000000", "integer", true},
	} {
		if got := valueHasJSONType(json.Number(tc.number), tc.kind); got != tc.want {
			t.Errorf("%s type%s=%v", tc.number, tc.kind, got)
		}
	}
	for _, tc := range []struct {
		number string
		schema map[string]any
		want   bool
	}{
		{"9007199254740993", map[string]any{"minimum": json.Number("9007199254740993")}, true},
		{"9007199254740992", map[string]any{"minimum": json.Number("9007199254740993")}, false},
		{"9007199254740993", map[string]any{"maximum": json.Number("9007199254740992")}, false},
		{"1.0000000000000001", map[string]any{"multipleOf": 1}, false},
		{"9.007199254740993e15", map[string]any{"multipleOf": json.Number("9007199254740993")}, true},
		{"-1e-400", map[string]any{"minimum": 0}, false},
		{"1e-1000000000", map[string]any{"minimum": 0}, false},
		{"0e-1000000000", map[string]any{"minimum": 0}, true},
		{"1.0000000000000001", map[string]any{"const": 1}, false},
		{"9007199254740993.0", map[string]any{"enum": []any{json.Number("9007199254740993")}}, true},
	} {
		if got := schemaOwnKeywordsMatch(json.Number(tc.number), tc.schema); got != tc.want {
			t.Errorf("%s schema%v=%v want%v", tc.number, tc.schema, got, tc.want)
		}
	}
}
