package registry

import (
	"errors"
	"maps"
	"testing"
)

// flagState is one way a layer or a top-level glob table can speak about the
// Disabled flag of the models under test.
type flagState int

const (
	flagAbsent flagState = iota
	flagDisableGlob
	flagEnableExact
	flagStates
)

// rowsFor builds the replay rows a flagState stands for, on top of base.
func rowsFor(base map[string]Model, state flagState) map[string]Model {
	rows := maps.Clone(base)
	if rows == nil {
		rows = map[string]Model{}
	}
	switch state {
	case flagDisableGlob:
		rows["claude-*"] = Model{ID: "claude-*", Disabled: new(true)}
	case flagEnableExact:
		rows["claude-opus-5"] = Model{ID: "claude-opus-5", Disabled: new(false)}
	}
	return rows
}

// modelDisabled is the cheap replay FindModel and InstanceModels use; Resolve
// replays the same layers in full. Over every shape of present and missing
// snapshot, overlay and config layers, with Disabled rows and top-level glob
// extras on each tag, the two must give the same verdict.
func TestModelDisabledAgreesWithResolveAcrossLayerShapes(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	rec, ok := r.recordFor("anthropic")
	if !ok {
		t.Fatal("no anthropic record")
	}
	tags := []string{LayerSnapshot, LayerOverlay, LayerConfig}
	baseLayers := append([]capLayer(nil), rec.layers...)
	baseTopGlobs := maps.Clone(r.topGlobs)
	t.Cleanup(func() { rec.layers, r.topGlobs = baseLayers, baseTopGlobs })

	// The record gets one layer per tag a mask keeps; a tag the mask drops is a
	// missing layer whose top-level extras still replay at its position.
	layerFor := func(tag string) capLayer {
		for _, l := range baseLayers {
			if l.tag == tag {
				return l
			}
		}
		return capLayer{tag: tag, owner: "anthropic", own: true}
	}
	models := []string{"claude-opus-5", "claude-sonnet-5"}
	disabledCount := 0
	for mask := 0; mask < 1<<len(tags); mask++ {
		for rowCombo := range pow(int(flagStates), len(tags)) {
			for globCombo := range pow(int(flagStates), len(tags)) {
				rec.layers = nil
				r.topGlobs = map[string]map[string]Model{}
				for i, tag := range tags {
					rowState := flagState(rowCombo / pow(int(flagStates), i) % int(flagStates))
					globState := flagState(globCombo / pow(int(flagStates), i) % int(flagStates))
					if globState != flagAbsent {
						r.topGlobs[tag] = rowsFor(nil, globState)
					}
					if mask&(1<<i) != 0 {
						layer := layerFor(tag)
						layer.rows = rowsFor(layer.rows, rowState)
						rec.layers = append(rec.layers, layer)
					}
				}
				for _, model := range models {
					ref := Ref{Instance: "anthropic", Model: model}
					_, err := r.resolveOn(rec, ref, nil)
					if err != nil && !errors.Is(err, ErrModelDisabled) {
						t.Fatalf("resolveOn(%s) = %v", model, err)
					}
					want := errors.Is(err, ErrModelDisabled)
					if want {
						disabledCount++
					}
					if got := r.modelDisabled(rec, ref, r.lookupRow(rec, model)); got != want {
						t.Fatalf("modelDisabled(%s) = %v, Resolve disabled = %v (layers %v, rows %d, globs %d)", model, got, want, layerTags(rec), rowCombo, globCombo)
					}
				}
			}
		}
	}
	if disabledCount == 0 {
		t.Fatal("no shape disabled a model; the cross-check exercised nothing")
	}
}

// TestModelDisabledPinnedVerdicts pins modelDisabled's answer for the layer,
// glob and alias shapes the ordering rules distinguish. The exhaustive shape
// test above compares the two replays with each other; a table of fixed
// answers catches the two drifting together.
func TestModelDisabledPinnedVerdicts(t *testing.T) {
	const codexBase = "[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk\"\n"
	cases := []struct {
		name            string
		config          string
		overlay         string
		instance, model string
		want            bool
	}{
		{
			name:     "no flags",
			instance: "anthropic", model: "claude-opus-4-6",
			want: false,
		},
		{
			name:     "config exact row disables",
			config:   "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: true,
		},
		{
			name:     "config provider glob disables, exact row re-enables",
			config:   "[providers.anthropic.models.\"claude-*\"]\ndisabled = true\n[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = false\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: false,
		},
		{
			name:     "config top glob disables",
			config:   "[models.\"*opus-4-6*\"]\ndisabled = true\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: true,
		},
		{
			name:     "config top glob disables, config exact row re-enables",
			config:   "[models.\"*opus-4-6*\"]\ndisabled = true\n[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = false\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: false,
		},
		{
			name:     "config exact row disables, config top glob re-enables then exact wins",
			config:   "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n[models.\"*opus-4-6*\"]\ndisabled = false\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: true,
		},
		{
			name:     "curated overlay top glob disables",
			overlay:  "[models.\"*opus-4-6*\"]\ndisabled = true\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: true,
		},
		{
			name:     "config exact row re-enables over curated overlay glob",
			overlay:  "[models.\"*opus-4-6*\"]\ndisabled = true\n",
			config:   "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = false\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: false,
		},
		{
			name:     "curated overlay provider glob disables",
			overlay:  "[providers.anthropic.models.\"claude-opus-4-6*\"]\ndisabled = true\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: true,
		},
		{
			// lookupRow takes the region-prefixed reference to the canonical
			// row, and its exact entry is what the replay applies.
			name:     "region-prefixed reference applies the canonical exact row",
			config:   "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n",
			instance: "anthropic", model: "us.claude-opus-4-6",
			want: true,
		},
		{
			// The flag lives on layer rows, so the order between layers is what
			// decides this: the curated overlay's disabled row must lose to the
			// user config's re-enabling row.
			name:     "config exact row re-enables a curated overlay exact row",
			overlay:  "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n",
			config:   "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = false\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: false,
		},
		{
			// The reverse of the case above on the same two layers: no
			// re-enable, so the overlay's row is the last writer.
			name:     "curated overlay exact row disables with no config row",
			overlay:  "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: true,
		},
		{
			// Different tags, different positions: a config top-level glob
			// replays after the overlay's rows, so it is the last writer here.
			name:     "config top glob re-enables a curated overlay exact row",
			overlay:  "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n",
			config:   "[models.\"*opus-4-6*\"]\ndisabled = false\n",
			instance: "anthropic", model: "claude-opus-4-6",
			want: false,
		},
		{
			// The reference matches only *4-6; the canonical row matches both,
			// so the longer pattern is a target match and applies last.
			name:     "region-prefixed reference replays canonical-row globs",
			config:   "[providers.anthropic.models.\"claude-opus-4-6*\"]\ndisabled = true\n[providers.anthropic.models.\"*4-6\"]\ndisabled = false\n",
			instance: "anthropic", model: "us.claude-opus-4-6",
			want: true,
		},
		{
			// Target-matching globs replay before reference-matching ones even
			// when shorter patterns sort first: the longer canonical-row
			// pattern is a target match, so it applies last and wins.
			name:     "target-matching glob replays after a shorter reference-only glob",
			config:   "[providers.anthropic.models.\"claude-opus-4-6*\"]\ndisabled = true\n[providers.anthropic.models.\"us.claude*\"]\ndisabled = false\n",
			instance: "anthropic", model: "us.claude-opus-4-6",
			want: false,
		},
		{
			name:     "same-provider alias inherits a disabled target",
			config:   "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n",
			instance: "anthropic", model: "house-model",
			want: true,
		},
		{
			name:     "same-provider alias of an enabled target stays enabled",
			config:   "[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n",
			instance: "anthropic", model: "house-model",
			want: false,
		},
		{
			name:     "cross-provider alias inherits a disabled target",
			config:   codexBase + "[providers.openai.models.\"gpt-5.6\"]\ndisabled = true\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n",
			instance: "mine", model: "house-model",
			want: true,
		},
		{
			name:     "cross-provider alias exact row re-enables over disabled target",
			config:   codexBase + "[providers.openai.models.\"gpt-5.6\"]\ndisabled = true\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\ndisabled = false\n",
			instance: "mine", model: "house-model",
			want: false,
		},
		{
			name:     "cross-provider alias top glob re-enables over disabled target",
			config:   codexBase + "[providers.openai.models.\"gpt-5.6\"]\ndisabled = true\n[models.\"house-*\"]\ndisabled = false\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n",
			instance: "mine", model: "house-model",
			want: false,
		},
		{
			name:     "cross-provider alias exact row disables over enabled target",
			config:   codexBase + "[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\ndisabled = true\n",
			instance: "mine", model: "house-model",
			want: true,
		},
		{
			// Globs replay against the alias's target row id too, so a glob
			// written for the target catches the alias.
			name:     "cross-provider alias is caught by a target-id glob",
			config:   codexBase + "[models.\"gpt-5.6*\"]\ndisabled = true\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n",
			instance: "mine", model: "house-model",
			want: true,
		},
		{
			// The glob lives on the alias's own record and matches only the
			// target id, while the target itself stays served: only the altID
			// rule can produce this verdict.
			name:     "provider-scoped target-id glob disables the alias alone",
			config:   codexBase + "[providers.mine.models.\"gpt-5.6*\"]\ndisabled = true\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n",
			instance: "mine", model: "house-model",
			want: true,
		},
		{
			// The same shape left enabled must stay served, so the case above is
			// not passing on a coincidental inherited verdict.
			name:     "provider-scoped target-id glob leaves the alias served when enabled",
			config:   codexBase + "[providers.mine.models.\"gpt-5.6*\"]\ndisabled = false\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n",
			instance: "mine", model: "house-model",
			want: false,
		},
		{
			// Target-matching globs replay before reference-matching ones, so the
			// glob written for the alias itself is the last writer.
			name:     "cross-provider alias reference glob beats target-id glob",
			config:   codexBase + "[models.\"gpt-5.6*\"]\ndisabled = true\n[models.\"house-*\"]\ndisabled = false\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n",
			instance: "mine", model: "house-model",
			want: false,
		},
		{
			// The same two globs with the flags the other way round: the
			// reference-matching glob still wins.
			name:     "cross-provider alias reference glob disables over target-id glob",
			config:   codexBase + "[models.\"gpt-5.6*\"]\ndisabled = false\n[models.\"house-*\"]\ndisabled = true\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n",
			instance: "mine", model: "house-model",
			want: true,
		},
		{
			name:     "curated cross-provider alias inherits a disabled target",
			config:   "[providers.openai.models.\"gpt-5.6-sol\"]\ndisabled = true\n",
			instance: "openai-codex", model: "gpt-5.6-sol",
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var extra []Option
			if tc.overlay != "" {
				extra = append(extra, WithOverlay(overlayWith(tc.overlay)))
			}
			r := fixtureLoad(t, nil, tc.config, extra...)
			rec, ok := r.recordFor(tc.instance)
			if !ok {
				t.Fatalf("no %s record", tc.instance)
			}
			ref := Ref{Instance: tc.instance, Model: tc.model}
			got := r.modelDisabled(rec, ref, r.lookupRow(rec, tc.model))
			if got != tc.want {
				t.Fatalf("modelDisabled(%s) = %v, want %v", ref, got, tc.want)
			}
			_, err := r.resolveOn(rec, ref, nil)
			resolveDisabled := errors.Is(err, ErrModelDisabled)
			if err != nil && !resolveDisabled {
				t.Fatalf("resolveOn(%s) = %v", ref, err)
			}
			if resolveDisabled != tc.want {
				t.Fatalf("Resolve disabled(%s) = %v, modelDisabled = %v, want %v", ref, resolveDisabled, got, tc.want)
			}
		})
	}
}

func pow(base, exp int) int {
	out := 1
	for range exp {
		out *= base
	}
	return out
}
