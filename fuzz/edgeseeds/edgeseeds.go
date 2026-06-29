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
		`"\ud83d\ude00"`,              // surrogate-pair escape (decodes to an emoji)
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
		"a = [1, \"x\", true]\n",                    // heterogeneous array (TOML 1.0 accepts)
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

// YAMLDoSCase is a YAML payload that gopkg.in/yaml.v3 rejects to resist denial
// of service, paired with a substring of the rejection error it produces.
type YAMLDoSCase struct {
	Name      string
	YAML      []byte
	ErrSubstr string
}

// YAMLDoS returns the denial-of-service payloads from gopkg.in/yaml.v3@v3.0.1
// limit_test.go — the library's own resistance suite: an alias-expansion bomb
// and three past-max-depth nestings. A consumer feeds these through its YAML
// decode path to prove it inherits yaml.v3's limits (a bounded error) rather
// than panicking, hanging, or exhausting memory. The payloads are ~1 MB each,
// so a test using them should gate behind testing.Short() as the original does.
func YAMLDoS() []YAMLDoSCase {
	const kb = 1024
	return []YAMLDoSCase{
		{
			Name:      "excessive-aliasing",
			YAML:      []byte(`{a: &a [{a}` + strings.Repeat(`,{a}`, 1000*kb/4-100) + `], b: &b [*a` + strings.Repeat(`,*a`, 99) + `]}`),
			ErrSubstr: "excessive aliasing",
		},
		{
			Name:      "deep-nested-slices",
			YAML:      []byte(strings.Repeat(`[`, 1000*kb)),
			ErrSubstr: "exceeded max depth",
		},
		{
			Name:      "deep-nested-maps",
			YAML:      []byte("x: " + strings.Repeat(`{`, 1000*kb)),
			ErrSubstr: "exceeded max depth",
		},
		{
			Name:      "deep-nested-indents",
			YAML:      []byte(strings.Repeat(`- `, 1000*kb)),
			ErrSubstr: "exceeded max depth",
		},
	}
}

// TOMLFeatureDoc is the seed input from BurntSushi/toml@v1.6.0 fuzz_test.go
// FuzzDecode: one document exercising most TOML features at once — string and
// multiline-string forms, every integer/float base, the full datetime/date/time
// zoo, inline tables, nested implicit tables, arrays of tables, and unicode
// escapes. It drives a decoder's whole lexer/parser in a single decode.
func TOMLFeatureDoc() []byte {
	return []byte(`
# This is an example TOML document which shows most of its features.

# Simple key/value with a string.
title = "TOML example \U0001F60A"

desc = """
An example TOML document. \
"""

# Array with integers and floats in the various allowed formats.
integers = [42, 0x42, 0o42, 0b0110]
floats   = [1.42, 1e-02]

# Array with supported datetime formats.
times = [
	2021-11-09T15:16:17+01:00,  # datetime with timezone.
	2021-11-09T15:16:17Z,       # UTC datetime.
	2021-11-09T15:16:17,        # local datetime.
	2021-11-09,                 # local date.
	15:16:17,                   # local time.
]

# Durations.
duration = ["4m49s", "8m03s", "1231h15m55s"]

# Table with inline tables.
distros = [
	{name = "Arch Linux", packages = "pacman"},
	{name = "Void Linux", packages = "xbps"},
	{name = "Debian",     packages = "apt"},
]

# Create new table; note the "servers" table is created implicitly.
[servers.alpha]
	ip        = '10.0.0.1'
	hostname  = 'server1'
	enabled   = false
[servers.beta]
	ip        = '10.0.0.2'
	hostname  = 'server2'
	enabled   = true

# Start a new table array; the "characters" table is created implicitly.
[[characters.star-trek]]
	name = "James Kirk"
	rank = "Captain \t"
[[characters.star-trek]]
	name = "Spock"
	rank = "Science officer"

[undecoded]
	key = "This table intentionally left undecoded"
`)
}

func toBytes(ss []string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}
