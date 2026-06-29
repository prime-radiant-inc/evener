package launchconfig

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"primeradiant.com/serf/fuzz/edgeseeds"
)

// FuzzLaunchConfigDecode drives the in-package TOML decoders for Layer
// (tomlDecode) and Meta (toml.Decode) over arbitrary bytes. It never touches the
// filesystem — no LoadLayer/LoadMeta/SaveLayer. The oracle is floor "no panic"
// plus a value-level round-trip fixed point for cleanly-decoding inputs. The
// first encode normalizes (empty collections with omitempty drop out, number
// formats canonicalize); like the Phase-0 JSON targets, we compare the two
// POST-normalization forms — decode → encode → decode#1 → encode → decode#2,
// asserting decode#1 == decode#2 via reflect.DeepEqual on the decoded values
// (compare values, not bytes; TOML map key order is non-deterministic).
func FuzzLaunchConfigDecode(f *testing.F) {
	f.Add(0, []byte("model = \"gpt-5.5\"\nreasoning_effort = \"high\"\n"))
	f.Add(0, []byte("model_fallbacks = [\"a\",\"b\"]\n"))
	f.Add(0, []byte("model_fallbacks = []\n"))
	f.Add(0, []byte("[env]\nFOO = \"bar\"\n"))
	f.Add(0, []byte("[[mcps]]\nname=\"x\"\ncommand=\"y\"\nargs=[\"-a\"]\n"))
	f.Add(1, []byte("schema = 1\ncwd = \"/w\"\ncreated_at = 2020-01-01T00:00:00Z\n"))
	f.Add(1, []byte("[trust]\nhashes = [\"abc\"]\ndecision = \"trusted\"\n"))
	// Degenerate / error shapes.
	f.Add(0, []byte(""))
	f.Add(0, []byte("= = ="))
	f.Add(0, []byte("max_rounds = \"not an int\""))
	// Generic TOML decoder stressors, fed to both the Layer and Meta arms.
	for _, s := range edgeseeds.TOML() {
		f.Add(0, s)
		f.Add(1, s)
	}

	f.Fuzz(func(t *testing.T, which int, raw []byte) {
		if which&1 == 0 {
			var l Layer
			if _, err := tomlDecode(raw, &l); err != nil {
				return // rejected input
			}
			l1 := reEncodeDecodeLayer(t, l, raw)
			l2 := reEncodeDecodeLayer(t, l1, raw)
			if !reflect.DeepEqual(l1, l2) {
				t.Fatalf("Layer round-trip not stable:\n input=%q\n once=%#v\n twice=%#v", raw, l1, l2)
			}
			return
		}

		var m Meta
		if _, err := toml.Decode(string(raw), &m); err != nil {
			return // rejected input
		}
		m1 := reEncodeDecodeMeta(t, m, raw)
		m2 := reEncodeDecodeMeta(t, m1, raw)
		if !metaEqual(m1, m2) {
			t.Fatalf("Meta round-trip not stable:\n input=%q\n once=%#v\n twice=%#v", raw, m1, m2)
		}
	})
}

// reEncodeDecodeLayer encodes l via the SaveLayer encoder and decodes the
// result via the LoadLayer/resolver decoder, returning the round-tripped value.
func reEncodeDecodeLayer(t *testing.T, l Layer, input []byte) Layer {
	t.Helper()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(l); err != nil {
		t.Fatalf("encode Layer failed: %v\n input=%q\n value=%#v", err, input, l)
	}
	var out Layer
	if _, err := tomlDecode(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode Layer failed: %v\n encoded=%q", err, buf.String())
	}
	return out
}

// reEncodeDecodeMeta is the Meta counterpart of reEncodeDecodeLayer.
func reEncodeDecodeMeta(t *testing.T, m Meta, input []byte) Meta {
	t.Helper()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		t.Fatalf("encode Meta failed: %v\n input=%q\n value=%#v", err, input, m)
	}
	var out Meta
	if _, err := toml.Decode(buf.String(), &out); err != nil {
		t.Fatalf("decode Meta failed: %v\n encoded=%q", err, buf.String())
	}
	return out
}

// metaEqual compares two Meta values using time.Equal for timestamp fields
// (reflect.DeepEqual is the wrong equality for time.Time: equal instants in
// different *time.Location representations are not DeepEqual) and DeepEqual for
// the rest.
func metaEqual(a, b Meta) bool {
	if a.Schema != b.Schema || a.CWD != b.CWD || !a.CreatedAt.Equal(b.CreatedAt) {
		return false
	}
	if a.Trust.Decision != b.Trust.Decision || a.Trust.Hash != b.Trust.Hash {
		return false
	}
	if !a.Trust.DecidedAt.Equal(b.Trust.DecidedAt) {
		return false
	}
	return reflect.DeepEqual(a.Trust.Hashes, b.Trust.Hashes)
}

// TestLaunchConfigThreeStateRoundTrip is the end-to-end proof that the
// ModelFallbacks *[]string refactor preserves all three states through the REAL
// encode/decode path, with no prepend quirk: nil stays nil (unset), non-nil
// empty stays non-nil empty (explicit clear), and a set chain stays set.
func TestLaunchConfigThreeStateRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   *[]string
	}{
		{"unset", nil},
		{"explicit-clear", &[]string{}},
		{"set", &[]string{"openai/gpt-5.4", "anthropic/claude-haiku-4-5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := toml.NewEncoder(&buf).Encode(Layer{ModelFallbacks: tc.in}); err != nil {
				t.Fatalf("encode: %v", err)
			}
			var got Layer
			if _, err := tomlDecode(buf.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v\n encoded=%q", err, buf.String())
			}
			switch {
			case tc.in == nil:
				if got.ModelFallbacks != nil {
					t.Fatalf("unset round-tripped to %#v, want nil\n encoded=%q", got.ModelFallbacks, buf.String())
				}
			case len(*tc.in) == 0:
				if got.ModelFallbacks == nil || len(*got.ModelFallbacks) != 0 {
					t.Fatalf("explicit-clear round-tripped to %#v, want non-nil empty\n encoded=%q", got.ModelFallbacks, buf.String())
				}
			default:
				if got.ModelFallbacks == nil || !reflect.DeepEqual(*got.ModelFallbacks, *tc.in) {
					t.Fatalf("set round-tripped to %#v, want %#v\n encoded=%q", got.ModelFallbacks, *tc.in, buf.String())
				}
			}
		})
	}
}
