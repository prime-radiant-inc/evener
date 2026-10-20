package registry

import (
	"reflect"
	"slices"
	"testing"
)

// foldFixtureProvider is the fixture-owned source whose replay rows a folded
// record must borrow. alpha carries the context window and header data the
// preservation cases check, beta is a second exact row, and a* is a glob row.
func foldFixtureProvider() Provider {
	return Provider{
		ID:       "fixture",
		Name:     "Fixture",
		Protocol: ProtocolOpenAIChat,
		Transport: Transport{
			BaseURL: "https://fixture.invalid/v1",
			Auth:    AuthNone,
		},
		Headers: map[string]string{"X-Provider": "fixture"},
		Caps:    Caps{Tools: new(true)},
		Models: map[string]Model{
			"alpha": {
				ID:      "alpha",
				Headers: map[string]string{"X-Alpha": "alpha"},
				Caps:    Caps{ContextWindow: new(8192)},
			},
			"beta": {
				ID:      "beta",
				Headers: map[string]string{"X-Beta": "beta"},
			},
			"a*": {
				ID:      "a*",
				Headers: map[string]string{"X-Glob": "a"},
			},
		},
	}
}

func TestFold_SharesCapabilityRows(t *testing.T) {
	fixture := foldFixtureProvider()
	cases := []struct {
		name string
		rows map[string]Model
	}{
		{name: "nil", rows: nil},
		{name: "allocated-empty", rows: map[string]Model{}},
		{name: "populated", rows: fixture.Models},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fixture
			src.Models = tc.rows
			rec := &record{
				name: "folded",
				head: Provider{ID: "folded", Models: map[string]Model{"existing": {ID: "existing"}}},
			}

			if err := rec.fold(src, LayerSnapshot, nil); err != nil {
				t.Fatalf("fold: %v", err)
			}
			if len(rec.layers) != 1 {
				t.Fatalf("layers = %d, want 1", len(rec.layers))
			}
			if reflect.ValueOf(rec.layers[0].rows).Pointer() != reflect.ValueOf(src.Models).Pointer() {
				t.Fatal("capability replay retained a duplicate model map")
			}
			if reflect.ValueOf(rec.head.Models).Pointer() == reflect.ValueOf(src.Models).Pointer() {
				t.Fatal("merged row metadata aliases the source map")
			}
			if _, ok := rec.head.Models["existing"]; !ok {
				t.Fatal("fold dropped the record's pre-existing merged row")
			}

			switch tc.name {
			case "nil":
				if rec.layers[0].rows != nil {
					t.Fatalf("nil source rows became %v", rec.layers[0].rows)
				}
			case "allocated-empty":
				if rec.layers[0].rows == nil || len(rec.layers[0].rows) != 0 {
					t.Fatalf("empty source rows became %v", rec.layers[0].rows)
				}
			case "populated":
				if got, want := sortedKeys(rec.head.Models), []string{"a*", "alpha", "beta", "existing"}; !slices.Equal(got, want) {
					t.Fatalf("merged rows = %v, want %v", got, want)
				}
				if got := rec.head.Models["alpha"].Headers["X-Alpha"]; got != "alpha" {
					t.Fatalf("merged alpha header = %q", got)
				}
				if got := rec.layers[0].rows["alpha"]; got.Caps.ContextWindow == nil || *got.Caps.ContextWindow != 8192 {
					t.Fatalf("replay alpha row = %+v", got)
				}

				// Mutating the merged head must not reach the borrowed source
				// rows: foldModel copies the row's own data into head.Models.
				rec.head.Models["alpha"].Headers["X-Alpha"] = "changed"
				delete(rec.head.Models, "beta")
				if got := src.Models["alpha"].Headers["X-Alpha"]; got != "alpha" {
					t.Fatalf("source alpha header changed to %q", got)
				}
				if _, ok := src.Models["beta"]; !ok {
					t.Fatal("source beta row disappeared after mutating the merged head")
				}
			}
		})
	}
}

func TestFold_SharedRowsSurviveInheritance(t *testing.T) {
	src := foldFixtureProvider()
	before := foldFixtureProvider()

	base := &record{name: "base", head: Provider{ID: "base", Models: map[string]Model{}}}
	if err := base.fold(src, LayerSnapshot, nil); err != nil {
		t.Fatalf("fold base: %v", err)
	}

	filtered := &record{name: "filtered"}
	filtered.inherit(base)
	filtered.keepInheritedRows([]string{"a*"})
	if err := filtered.fold(Provider{
		ID: "filtered",
		Models: map[string]Model{
			"a-child": {ID: "a-child", Headers: map[string]string{"X-Child": "filtered"}},
		},
	}, LayerConfig, nil); err != nil {
		t.Fatalf("fold filtered child: %v", err)
	}

	detached := &record{name: "detached"}
	detached.inherit(base)
	if err := detached.fold(Provider{
		ID:            "detached",
		InheritModels: new(false),
		Models: map[string]Model{
			"b-child": {ID: "b-child", Headers: map[string]string{"X-Child": "detached"}},
		},
	}, LayerConfig, nil); err != nil {
		t.Fatalf("fold detached child: %v", err)
	}

	if got, want := sortedKeys(base.head.Models), []string{"a*", "alpha", "beta"}; !slices.Equal(got, want) {
		t.Fatalf("base rows = %v, want %v", got, want)
	}
	if got, want := sortedKeys(filtered.head.Models), []string{"a*", "a-child", "alpha"}; !slices.Equal(got, want) {
		t.Fatalf("filtered child rows = %v, want %v", got, want)
	}
	if got, want := sortedKeys(detached.head.Models), []string{"b-child"}; !slices.Equal(got, want) {
		t.Fatalf("detached child rows = %v, want %v", got, want)
	}
	if got, want := sortedKeys(filtered.layers[0].rows), []string{"a*", "alpha"}; !slices.Equal(got, want) {
		t.Fatalf("filtered inherited layer rows = %v, want %v", got, want)
	}
	if detached.layers[0].rows != nil {
		t.Fatalf("inherit_models = false left inherited rows %v", detached.layers[0].rows)
	}

	if got := base.layers[0].rows["alpha"]; got.Caps.ContextWindow == nil || *got.Caps.ContextWindow != 8192 {
		t.Fatalf("base alpha fact = %+v", got)
	}
	if got := filtered.layers[0].rows["alpha"]; got.Caps.ContextWindow == nil || *got.Caps.ContextWindow != 8192 {
		t.Fatalf("filtered alpha fact = %+v", got)
	}
	if got := src.Models["alpha"].Caps.ContextWindow; got == nil || *got != 8192 {
		t.Fatalf("source alpha fact = %v", got)
	}

	// The filtered child rebuilt its inherited row map rather than editing
	// the base's or the source's map in place.
	if reflect.ValueOf(filtered.layers[0].rows).Pointer() == reflect.ValueOf(base.layers[0].rows).Pointer() {
		t.Fatal("filtered child aliases the base layer's row map")
	}
	if !reflect.DeepEqual(src, before) {
		t.Fatalf("fixture source changed:\n got %#v\nwant %#v", src, before)
	}
}

func TestLoad_SharedRowsPreserveResolution(t *testing.T) {
	check := func(t *testing.T, r *Registry) {
		t.Helper()
		res := mustResolve(t, r, "openai/gpt-5.6")
		if res.ProviderID != "openai" || res.Protocol != ProtocolOpenAIResponses || res.ModelID != "gpt-5.6" {
			t.Fatalf("resolved = provider %q protocol %q model %q, want openai/openai-responses/gpt-5.6", res.ProviderID, res.Protocol, res.ModelID)
		}
		if res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 1050000 {
			t.Fatalf("context window = %v, want 1050000", res.Caps.ContextWindow)
		}
	}

	r := fixtureLoad(t, nil, "")
	view, ok := r.Provider("openai")
	if !ok {
		t.Fatal("openai provider is missing")
	}
	delete(view.Models, "gpt-5.6")
	view.Models["synthetic"] = Model{ID: "synthetic", Protocol: ProtocolGoogle, Headers: map[string]string{"X-Mutated": "view"}}
	for id, m := range view.Models {
		m.Headers = map[string]string{"X-Mutated": id}
		view.Models[id] = m
	}

	check(t, r)
	check(t, fixtureLoad(t, nil, ""))
}

func TestLoad_RowWithAbsentTransportPresetFails(t *testing.T) {
	r, err := Load(
		WithSnapshot(fixtureBytes(t)),
		WithOverlay(overlayWith("[providers.openai.models.\"absent-preset\"]\ntransport = \"does-not-exist\"\n")),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithStateRoot(t.TempDir()),
		WithOffline(true),
		WithoutCache(),
	)
	if err == nil {
		t.Fatal("Load accepted a row naming an absent transport preset")
	}
	if r != nil {
		t.Fatal("Load returned a registry alongside the preset error")
	}
}

var registryBenchmarkSink *Registry
var resolvedBenchmarkSink Resolved
var modelIDsBenchmarkSink []string

func BenchmarkLoadEmbeddedWarm(b *testing.B) {
	if _, err := loadEmbeddedCatalog(); err != nil {
		b.Fatal(err)
	}
	if _, err := loadEmbeddedOverlay(); err != nil {
		b.Fatal(err)
	}
	stateRoot := b.TempDir()
	for b.Loop() {
		r, err := Load(
			WithStateRoot(stateRoot),
			WithEnv(mapEnv(nil)),
			WithNoUserLayer(),
			WithOffline(true),
			WithoutCache(),
		)
		if err != nil {
			b.Fatal(err)
		}
		registryBenchmarkSink = r
	}
}

func BenchmarkCatalogLookupAndList(b *testing.B) {
	registry := embeddedLoad(b)
	b.Run("resolve", func(b *testing.B) {
		for b.Loop() {
			res, err := registry.Resolve("openai/gpt-5.6")
			if err != nil {
				b.Fatal(err)
			}
			resolvedBenchmarkSink = res
		}
	})
	b.Run("model-ids", func(b *testing.B) {
		for b.Loop() {
			ids, err := registry.ModelIDs("openai")
			if err != nil {
				b.Fatal(err)
			}
			modelIDsBenchmarkSink = ids
		}
	})
}
