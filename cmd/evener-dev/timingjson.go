package dev

// The budget file is hand-edited and a bless rewrites it, so timing-compare
// needs the same JSON semantics the retired inline Python comparator had:
// duplicate keys keep the last value, NaN/Infinity literals are accepted (as
// Python's json module accepts them), and a bless reproduces Python's
// json.dump(indent=1) layout instead of Go's map ordering and float formatting.
// A large enough integer is no more usable as seconds than Infinity is, so the
// parser keeps the literal and lets the comparator reject it by name.

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type jsonKind uint8

const (
	jsonNull jsonKind = iota
	jsonBool
	jsonNumber
	jsonString
	jsonArray
	jsonObject
)

type jsonMember struct {
	key   string
	value *jsonValue
}

// jsonValue is one JSON value. Objects keep their member list in file order
// with duplicates preserved; a lookup by key uses the last occurrence, which is
// what Python's json.load keeps.
type jsonValue struct {
	kind jsonKind
	b    bool
	num  string // the value's canonical json.dump() form, already Python-formatted
	str  string
	arr  []*jsonValue
	obj  []jsonMember
}

func newJSONObject() *jsonValue         { return &jsonValue{kind: jsonObject} }
func newJSONString(s string) *jsonValue { return &jsonValue{kind: jsonString, str: s} }
func newJSONNumber(f float64) *jsonValue {
	return &jsonValue{kind: jsonNumber, num: pythonFloat(f)}
}

// member returns the last value stored under key, matching json.load's
// last-occurrence-wins rule.
func (v *jsonValue) member(key string) (*jsonValue, bool) {
	if v == nil || v.kind != jsonObject {
		return nil, false
	}
	for i := range slices.Backward(v.obj) {
		if v.obj[i].key == key {
			return v.obj[i].value, true
		}
	}
	return nil, false
}

func (v *jsonValue) has(key string) bool {
	_, ok := v.member(key)
	return ok
}

// set updates the first occurrence in place (Python dict update keeps the
// original position) or appends a new member.
func (v *jsonValue) set(key string, value *jsonValue) {
	for i := range v.obj {
		if v.obj[i].key == key {
			v.obj[i].value = value
			return
		}
	}
	v.obj = append(v.obj, jsonMember{key: key, value: value})
}

// typeName is the Python type name seconds_error reports for a value.
func (v *jsonValue) typeName() string {
	switch v.kind {
	case jsonBool:
		return "bool"
	case jsonNumber:
		if strings.ContainsAny(v.num, ".eE") || v.num == "NaN" || v.num == "Infinity" || v.num == "-Infinity" {
			return "float"
		}
		return "int"
	case jsonString:
		return "str"
	case jsonArray:
		return "list"
	case jsonObject:
		return "dict"
	default:
		return "NoneType"
	}
}

// float returns the value as a float64. Callers validate with secondsError
// first; an out-of-range literal still yields its IEEE infinity.
func (v *jsonValue) float() (float64, bool) {
	if v.kind != jsonNumber {
		return 0, false
	}
	f, err := strconv.ParseFloat(v.num, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return f, true
}

// secondsError is the one definition of "cannot serve as a number of seconds",
// shared by package budgets, wall budgets, the flat ceiling and per-test
// ceilings. It matches the Python comparator's cases: bools, strings, nulls,
// containers, non-finite numbers, and integers too large to convert to a float.
func secondsError(v *jsonValue) string {
	switch v.kind {
	case jsonBool:
		return "got bool"
	case jsonString:
		return "got str"
	case jsonNull:
		return "got NoneType"
	case jsonArray:
		return "got list"
	case jsonObject:
		return "got dict"
	case jsonNumber:
		switch v.num {
		case "NaN":
			return "got NaN"
		case "Infinity":
			return "got Infinity"
		case "-Infinity":
			return "got -Infinity"
		}
		f, err := strconv.ParseFloat(v.num, 64)
		if err != nil {
			if errors.Is(err, strconv.ErrRange) {
				if !strings.ContainsAny(v.num, ".eE") {
					return "too large for a float"
				}
				if f < 0 {
					return "got -Infinity"
				}
				return "got Infinity"
			}
			return "got " + v.num
		}
		switch {
		case math.IsNaN(f):
			return "got NaN"
		case math.IsInf(f, 1):
			return "got Infinity"
		case math.IsInf(f, -1):
			return "got -Infinity"
		}
	}
	return ""
}

// jsonString renders v as Python's json.dump would, for the bless path.
func (v *jsonValue) jsonString() string {
	var b strings.Builder
	v.appendJSON(&b, 0)
	return b.String()
}

func (v *jsonValue) appendJSON(b *strings.Builder, depth int) {
	switch v.kind {
	case jsonNull:
		b.WriteString("null")
	case jsonBool:
		if v.b {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case jsonNumber:
		b.WriteString(v.num)
	case jsonString:
		b.WriteString(jsonQuote(v.str))
	case jsonArray:
		if len(v.arr) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range v.arr {
			b.WriteString(strings.Repeat(" ", depth+1))
			item.appendJSON(b, depth+1)
			if i < len(v.arr)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(" ", depth))
		b.WriteByte(']')
	case jsonObject:
		if len(v.obj) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, m := range v.obj {
			b.WriteString(strings.Repeat(" ", depth+1))
			b.WriteString(jsonQuote(m.key))
			b.WriteString(": ")
			m.value.appendJSON(b, depth+1)
			if i < len(v.obj)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(" ", depth))
		b.WriteByte('}')
	}
}

// pythonFloat formats a float the way Python's repr does, which is what
// json.dump writes: 2.0 keeps its .0, and a value outside the fixed-point
// window uses an exponent.
func pythonFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	abs := math.Abs(f)
	var s string
	if abs != 0 && (abs < 1e-4 || abs >= 1e16) {
		s = strconv.FormatFloat(f, 'e', -1, 64)
	} else {
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// pythonJSONNumber canonicalises one number literal the way json.load followed
// by json.dump would: an integer literal stays an integer, a float literal
// becomes Python's float representation.
func pythonJSONNumber(raw string) string {
	switch raw {
	case "NaN", "Infinity", "-Infinity":
		return raw
	}
	if !strings.ContainsAny(raw, ".eE") {
		if raw == "-0" {
			return "0"
		}
		return raw
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return raw
	}
	return pythonFloat(f)
}

// jsonQuote renders a string the way Python's json.dumps does with its default
// ensure_ascii=True: non-ASCII runes become lower-case \u escapes.
func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			switch {
			case r < 0x20 || r > 0x7e:
				if r > 0xffff {
					r -= 0x10000
					hi, lo := uint16(0xd800+(r>>10)), uint16(0xdc00+(r&0x3ff))
					fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
				} else {
					fmt.Fprintf(&b, `\u%04x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// parseJSONDocument parses one JSON value, accepting Python's NaN and
// Infinity literals and preserving duplicate object keys.
func parseJSONDocument(data []byte) (*jsonValue, error) {
	p := jsonParser{s: string(data)}
	p.skipSpace()
	if p.i >= len(p.s) {
		return nil, p.errorf("expecting value")
	}
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.i != len(p.s) {
		return nil, p.errorf("extra data")
	}
	return v, nil
}

type jsonParser struct {
	s string
	i int
}

type jsonSyntaxError struct {
	offset int
	msg    string
}

func (e *jsonSyntaxError) Error() string { return fmt.Sprintf("%s at offset %d", e.msg, e.offset) }

func (p *jsonParser) errorf(msg string) error { return &jsonSyntaxError{offset: p.i, msg: msg} }

func (p *jsonParser) skipSpace() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *jsonParser) parseValue() (*jsonValue, error) {
	if p.i >= len(p.s) {
		return nil, p.errorf("expecting value")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.parseObject()
	case c == '[':
		return p.parseArray()
	case c == '"':
		s, err := p.parseString()
		if err != nil {
			return nil, err
		}
		return newJSONString(s), nil
	case strings.HasPrefix(p.s[p.i:], "true"):
		p.i += len("true")
		return &jsonValue{kind: jsonBool, b: true}, nil
	case strings.HasPrefix(p.s[p.i:], "false"):
		p.i += len("false")
		return &jsonValue{kind: jsonBool}, nil
	case strings.HasPrefix(p.s[p.i:], "null"):
		p.i += len("null")
		return &jsonValue{kind: jsonNull}, nil
	case strings.HasPrefix(p.s[p.i:], "NaN"):
		p.i += len("NaN")
		return &jsonValue{kind: jsonNumber, num: "NaN"}, nil
	case strings.HasPrefix(p.s[p.i:], "Infinity"):
		p.i += len("Infinity")
		return &jsonValue{kind: jsonNumber, num: "Infinity"}, nil
	case strings.HasPrefix(p.s[p.i:], "-Infinity"):
		p.i += len("-Infinity")
		return &jsonValue{kind: jsonNumber, num: "-Infinity"}, nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.parseNumber()
	}
	return nil, p.errorf("expecting value")
}

func (p *jsonParser) parseObject() (*jsonValue, error) {
	p.i++ // {
	obj := newJSONObject()
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return obj, nil
	}
	for {
		p.skipSpace()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, p.errorf("expecting property name")
		}
		key, err := p.parseString()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, p.errorf("expecting ':'")
		}
		p.i++
		p.skipSpace()
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		obj.obj = append(obj.obj, jsonMember{key: key, value: value})
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, p.errorf("expecting ',' or '}'")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return obj, nil
		default:
			return nil, p.errorf("expecting ',' or '}'")
		}
	}
}

func (p *jsonParser) parseArray() (*jsonValue, error) {
	p.i++ // [
	arr := &jsonValue{kind: jsonArray}
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return arr, nil
	}
	for {
		p.skipSpace()
		item, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		arr.arr = append(arr.arr, item)
		p.skipSpace()
		if p.i >= len(p.s) {
			return nil, p.errorf("expecting ',' or ']'")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return arr, nil
		default:
			return nil, p.errorf("expecting ',' or ']'")
		}
	}
}

func (p *jsonParser) parseString() (string, error) {
	if p.i >= len(p.s) || p.s[p.i] != '"' {
		return "", p.errorf("expecting string")
	}
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String(), nil
		case c == '\\':
			p.i++
			if p.i >= len(p.s) {
				return "", p.errorf("unterminated escape")
			}
			switch p.s[p.i] {
			case '"', '\\', '/':
				b.WriteByte(p.s[p.i])
				p.i++
			case 'b':
				b.WriteByte('\b')
				p.i++
			case 'f':
				b.WriteByte('\f')
				p.i++
			case 'n':
				b.WriteByte('\n')
				p.i++
			case 'r':
				b.WriteByte('\r')
				p.i++
			case 't':
				b.WriteByte('\t')
				p.i++
			case 'u':
				r, err := p.parseUnicodeEscape()
				if err != nil {
					return "", err
				}
				if utf16.IsSurrogate(r) && r >= 0xd800 && r <= 0xdbff {
					save := p.i
					if p.i+1 < len(p.s) && p.s[p.i] == '\\' && p.s[p.i+1] == 'u' {
						p.i++
						lo, err := p.parseUnicodeEscape()
						if err == nil && lo >= 0xdc00 && lo <= 0xdfff {
							r = utf16.DecodeRune(r, lo)
						} else {
							p.i = save
						}
					}
				}
				b.WriteRune(r)
			default:
				return "", p.errorf("invalid escape")
			}
		case c < 0x20:
			return "", p.errorf("invalid control character")
		default:
			r, size := utf8.DecodeRuneInString(p.s[p.i:])
			if r == utf8.RuneError && size == 1 {
				b.WriteByte(c)
				p.i++
			} else {
				b.WriteRune(r)
				p.i += size
			}
		}
	}
	return "", p.errorf("unterminated string")
}

func (p *jsonParser) parseUnicodeEscape() (rune, error) {
	// p.i is at 'u'.
	if p.i+5 > len(p.s) {
		return 0, p.errorf("invalid \\u escape")
	}
	n, err := strconv.ParseUint(p.s[p.i+1:p.i+5], 16, 16)
	if err != nil {
		return 0, p.errorf("invalid \\u escape")
	}
	p.i += 5
	return rune(n), nil
}

func (p *jsonParser) parseNumber() (*jsonValue, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.s) {
		return nil, p.errorf("invalid number")
	}
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.s[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		return nil, p.errorf("invalid number")
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if digits() == 0 {
			return nil, p.errorf("invalid number")
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			return nil, p.errorf("invalid number")
		}
	}
	return &jsonValue{kind: jsonNumber, num: pythonJSONNumber(p.s[start:p.i])}, nil
}
