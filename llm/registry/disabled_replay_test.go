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

func pow(base, exp int) int {
	out := 1
	for range exp {
		out *= base
	}
	return out
}
