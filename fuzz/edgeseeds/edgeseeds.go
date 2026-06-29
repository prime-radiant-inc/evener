// Package edgeseeds provides generic, schema-independent corpus seeds for
// fuzz targets that decode a text format (JSON, TOML, YAML frontmatter).
//
// These are decoder stressors, not valid documents: deep nesting, magic
// numbers, surrogate halves, duplicate keys, BOMs, format-specific traps. A
// fuzz target seeds them via f.Add to give the mutator a head start on the
// hostile inputs that exercise the format library's own edges rather than only
// the target's schema. Most are rejected by a well-behaved decoder and so
// exercise the no-panic / structured-error floor; a few that decode cleanly
// exercise the round-trip oracle.
//
// Sizes are kept modest (nesting in the tens, strings in the low kilobytes) on
// purpose: seeds run on every `go test`, so a real billion-laughs bomb here
// would tax the suite. The fuzzer mutates deeper from these starting points,
// and the memory cap bounds any runaway.
//
// This package imports nothing from primeradiant.com/serf — it lives in the
// portable fuzz module so any target in any module can seed from one source.
package edgeseeds

import "strings"

// bom is the UTF-8 byte-order mark, a common prefix trap for text decoders.
const bom = "\ufeff"

// JSON returns generic stressors for an encoding/json decoder. They are valid
// or near-valid JSON shaped to hit the library's edges (deep nesting, numeric
// extremes, surrogate handling, duplicate keys, trailing data, embedded NUL)
// independent of any particular target schema.
func JSON() [][]byte {
	return toBytes([]string{
		"[" + strings.Repeat("[", 127) + strings.Repeat("]", 127) + "]", // deep arrays
		strings.Repeat(`{"a":`, 64) + "1" + strings.Repeat("}", 64),     // deep objects
		`1e1000`,                      // overflow exponent
		`1e-1000`,                     // underflow exponent
		`100000000000000000000000000`, // oversized integer
		`-0`,                          // negative zero int
		`-0.0`,                        // negative zero float
		`"\ud800"`,                    // lone high surrogate
		`"\udc00"`,                    // lone low surrogate
		`"😀"`,                         // valid surrogate pair (emoji)
		`"\u0000"`,                    // NUL via \u escape (decodes to NUL)
		`{"a":1,"a":2}`,               // duplicate keys
		bom + "{}",                    // UTF-8 BOM prefix
		`{"a":1} trailing`,            // trailing data after value
		"\"a\nb\"",                    // raw newline in string (invalid)
		`+1`,                          // leading-plus number (invalid)
		`NaN`,                         // bareword NaN (invalid)
		`Infinity`,                    // bareword Infinity (invalid)
		`{"k":"` + strings.Repeat("x", 4096) + `"}`, // very long string value
		`[{"a":[{"b":[{}]}]}]`,                      // mixed deep nesting
	})
}

// TOML returns generic stressors for a TOML decoder (the BurntSushi/toml
// library serf uses): duplicate keys and tables, dotted/quoted keys, datetime
// and number extremes, multiline strings, inline tables, arrays of tables, and
// a BOM prefix.
func TOML() [][]byte {
	return toBytes([]string{
		"a = 1\na = 2\n",                            // duplicate bare key (rejected)
		"[t]\nx = 1\n[t]\ny = 2\n",                  // duplicate table (rejected)
		"[a.b.c.d.e.f.g.h]\nx = 1\n",                // deep dotted table
		"a = 9999999999999999999999999\n",           // integer overflow
		"a = nan\n",                                 // float NaN
		"a = inf\n",                                 // float +inf
		"a = -inf\n",                                // float -inf
		"a = 2020-01-01T00:00:00.999999999+05:30\n", // offset datetime, nanos
		"a = 2020-01-01\n",                          // local date
		"a = 00:00:00.000001\n",                     // local time
		"a = 2020-01-01T00:00:00\n",                 // local datetime
		"a = \"\"\"\nline1\nline2\n\"\"\"\n",        // multiline basic string
		"a = '''\nraw\\nnot escaped\n'''\n",         // multiline literal string
		"a = \"\\uFFFF\"\n",                         // BMP unicode escape
		"a = \"\\U0001F600\"\n",                     // astral unicode escape
		"\"a.b\" = 1\n'k k' = 2\n",                  // quoted/dotted keys
		"a = {b = 1, c = [1, 2]}\n",                 // inline table
		"[[t]]\nx = 1\n[[t]]\nx = 2\n",              // array of tables
		"a = [1, \"x\", true]\n",                    // mixed-type array (rejected)
		"a = [[[[[]]]]]\n",                          // deep nested arrays
		bom + "a = 1\n",                             // BOM prefix
		"\"\" = 1\n",                                // empty key
	})
}

// FrontmatterYAML returns whole Markdown documents (---fenced YAML--- + body)
// that stress the gopkg.in/yaml.v3 decoder behind the frontmatter parsers:
// anchors/aliases, merge keys, tab indentation, the boolean/null/number
// coercion zoo, flow style, duplicate keys, explicit tags, and an embedded
// document marker that probes the --- framing boundary itself.
func FrontmatterYAML() []string {
	return []string{
		"---\na: &x 1\nb: *x\n---\nbody\n",                        // anchor + alias
		"---\nbase: &b {k: 1}\nm:\n  <<: *b\n---\nbody\n",         // merge key
		"---\na:\n\tb: 1\n---\nbody\n",                            // tab indentation (invalid)
		"---\na: yes\nb: no\nc: on\nd: off\ne: True\n---\nbody\n", // boolean zoo
		"---\na: ~\nb: null\nc:\nd: Null\n---\nbody\n",            // null zoo
		"---\na: 0o17\nb: 0x1F\nc: 1_000\n---\nbody\n",            // octal/hex/underscore
		"---\na: .inf\nb: -.inf\nc: .nan\n---\nbody\n",            // float specials
		"---\nm: {a: 1, b: [1, 2, 3]}\n---\nbody\n",               // flow style
		"---\na: 1\na: 2\n---\nbody\n",                            // duplicate keys
		"---\na: !!str 1\nb: !!int \"5\"\n---\nbody\n",            // explicit tags
		"---\n\"a: b\": 1\n---\nbody\n",                           // quoted key with colon
		"---\na: 1\n...\nb: 2\n---\nbody\n",                       // embedded doc-end marker
		"---\na: " + strings.Repeat("z", 4096) + "\n---\nbody\n",  // very long scalar
		"---\n" + strings.Repeat("a:\n ", 32) + "1\n---\nbody\n",  // deep nested mapping
	}
}

func toBytes(ss []string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}
