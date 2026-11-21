package dev

// The budget file is hand-edited and a bless rewrites it, so timing-compare
// reads it through encoding/json with two additions the standard decoder does
// not offer: it preserves each object's members in file order with duplicates
// intact (a bless rewrites the budget where the hand edit put each key, and the
// duplicate-ceiling check needs to see both occurrences), and it refuses the
// Python-only NaN and Infinity literals the file must not carry. A lookup by
// key returns the last occurrence, the rule a standard JSON parser applies.
//
// Nothing here mimics Python's number or string formatting. The encoder writes
// the checked-in one-space indent, a measured duration keeps a .0 when it is
// whole so the file stays in its float shape, and a non-finite duration is
// refused rather than written as a literal the next run would reject.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// jsonMember is one object member in file order.
type jsonMember struct {
	key   string
	value any
}

// jsonObject is a JSON object that keeps its members in file order with
// duplicates preserved. A lookup by key returns the last occurrence.
type jsonObject struct {
	members []jsonMember
}

func newJSONObject() *jsonObject { return &jsonObject{} }

func (o *jsonObject) member(key string) (any, bool) {
	for i := len(o.members) - 1; i >= 0; i-- {
		if o.members[i].key == key {
			return o.members[i].value, true
		}
	}
	return nil, false
}

func (o *jsonObject) has(key string) bool {
	_, ok := o.member(key)
	return ok
}

// set updates the first occurrence in place, so a bless rewrites a member where
// the hand edit put it, or appends a new member.
func (o *jsonObject) set(key string, value any) {
	for i := range o.members {
		if o.members[i].key == key {
			o.members[i].value = value
			return
		}
	}
	o.members = append(o.members, jsonMember{key: key, value: value})
}

// MarshalJSON writes the members in file order. The caller indents with
// encoding/json, so these compact bytes become the checked-in layout.
func (o *jsonObject) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o.members {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshalJSON(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		value, err := marshalJSON(m.value)
		if err != nil {
			return nil, err
		}
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalJSON encodes one value without escaping HTML, so a hand-edited string
// round-trips unchanged.
func marshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// parseJSONDocument decodes one JSON value. encoding/json supplies the syntax,
// so the Python-only NaN and Infinity literals are rejected; UseNumber keeps
// every numeric literal as written so a bless preserves the file's floats.
func parseJSONDocument(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	doc, err := decodeJSONValue(dec)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("unexpected end of JSON input")
		}
		return nil, err
	}
	switch _, err := dec.Token(); {
	case errors.Is(err, io.EOF):
		return doc, nil
	case err != nil:
		return nil, err
	default:
		return nil, errors.New("extra data after the JSON value")
	}
}

// decodeJSONValue reads one value from dec as a jsonObject, []any, or the
// scalar token the decoder returns. Objects keep their members in file order.
func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := newJSONObject()
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("object key is %v, not a string", keyTok)
			}
			value, err := decodeJSONValue(dec)
			if err != nil {
				return nil, err
			}
			obj.members = append(obj.members, jsonMember{key: key, value: value})
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case '[':
		var arr []any
		for dec.More() {
			item, err := decodeJSONValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, item)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return arr, nil
	}
	return nil, fmt.Errorf("unexpected %v", delim)
}

// typeName is the type name the malformed-budget messages report.
func typeName(v any) string {
	switch t := v.(type) {
	case bool:
		return "bool"
	case json.Number:
		if strings.ContainsAny(string(t), ".eE") {
			return "float"
		}
		return "int"
	case string:
		return "str"
	case []any:
		return "list"
	case *jsonObject:
		return "dict"
	case nil:
		return "NoneType"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// secondsValue parses v as a number of seconds, or names why it cannot: bools,
// strings, nulls, containers and non-finite numbers are not a duration. It is
// the one definition shared by package budgets, wall budgets, the flat ceiling
// and per-test ceilings.
func secondsValue(v any) (float64, string) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, "got " + typeName(v)
	}
	f, err := n.Float64()
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			if !strings.ContainsAny(string(n), ".eE") {
				return 0, "too large for a float"
			}
			if f < 0 {
				return 0, "got -Infinity"
			}
			return 0, "got Infinity"
		}
		return 0, "got " + string(n)
	}
	switch {
	case math.IsNaN(f):
		return 0, "got NaN"
	case math.IsInf(f, 1):
		return 0, "got Infinity"
	case math.IsInf(f, -1):
		return 0, "got -Infinity"
	}
	return f, ""
}

// secondsMap validates every member of a budget object as seconds and returns
// them keyed by name. A non-empty problem names the first offending member.
func secondsMap(object *jsonObject) (map[string]float64, string) {
	out := make(map[string]float64, len(object.members))
	for _, m := range object.members {
		seconds, problem := secondsValue(m.value)
		if problem != "" {
			return nil, "[" + strconv.Quote(m.key) + "] must be a number of seconds (" + problem + ")"
		}
		out[m.key] = seconds
	}
	return out, ""
}

// budgetNumber renders one measured duration. A whole number keeps a .0 so the
// file's float-shaped entries stay floats (2.0, not 2).
func budgetNumber(seconds float64) json.Number {
	s := strconv.FormatFloat(seconds, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return json.Number(s)
}

// writeTimingBudget writes the budget in the checked-in one-space indent.
func writeTimingBudget(path string, budget *jsonObject) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(budget); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}
