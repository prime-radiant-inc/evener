package registry

import (
	"errors"
	"testing"
)

// A registry whose catalog and overlay carry no implicit provider and whose
// environment names nothing has no instance to default to.
func tinyCatalogRegistry(t *testing.T) *Registry {
	t.Helper()
	body := []byte(`{"tinyprov":{"npm":"@ai-sdk/openai-compatible","api":"https://example/v1","models":{"m":{"modalities":{"output":["text"]}}}}}`)
	r, err := Load(
		WithSnapshot(body),
		WithOverlay([]byte("")),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithStateRoot(t.TempDir()),
		WithOffline(true),
		WithoutCache(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIsChatModelIDEmpty(t *testing.T) {
	t.Parallel()
	if IsChatModelID("") {
		t.Fatal("an empty id names no model")
	}
	if !IsChatModelID("gpt-4o") {
		t.Fatal("a chat id must pass")
	}
}

func TestResolveEmptyModelReference(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, "")
	if _, err := r.Resolve("/"); err == nil {
		t.Fatal("an empty model reference must be refused")
	}
	if _, err := r.Resolve("openai/"); err == nil {
		t.Fatal("a reference with an empty model half must be refused")
	}
}

// With no default instance, a bare model reference has nowhere to go — and
// the model-facts seam reports the same refusal.
func TestResolveWithoutDefaultInstance(t *testing.T) {
	t.Parallel()
	r := tinyCatalogRegistry(t)
	if _, err := r.Resolve("m"); !errors.Is(err, errNoDefaultInstance) {
		t.Fatalf("Resolve with no default instance: err = %v", err)
	}
	if _, err := r.ResolveInstanceModelFacts("", "m"); !errors.Is(err, errNoDefaultInstance) {
		t.Fatalf("ResolveInstanceModelFacts with no default instance: err = %v", err)
	}
	if _, err := r.ResolveInstanceModelFacts("nope", "m"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("ResolveInstanceModelFacts(unknown): err = %v", err)
	}
}

func TestResolveCatalogBrowseAPIs(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, "")
	if _, err := r.ResolveCatalog("nope", "m"); err == nil {
		t.Fatal("an unknown provider id must be refused")
	}
	ids, err := r.CatalogModelIDs("openai")
	if err != nil || len(ids) == 0 {
		t.Fatalf("CatalogModelIDs(openai) = (%v, %v)", ids, err)
	}
	res, err := r.ResolveCatalog("openai", ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if res.Instance != "openai" {
		t.Fatalf("ResolveCatalog resolved instance %q", res.Instance)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("a curated id that is not an instance must carry a warning")
	}
}

func TestModelListingUnknownInstance(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, "")
	if _, err := r.InstanceModels("nope"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("InstanceModels(unknown): err = %v", err)
	}
	if _, err := r.ModelIDs("nope"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("ModelIDs(unknown): err = %v", err)
	}
	if _, err := r.CatalogModelIDs("nope"); err == nil {
		t.Fatal("CatalogModelIDs(unknown) must fail")
	}
}

// A live-only id has no config row a toggle could write, so AliasTarget
// answers the verbatim reference.
func TestAliasTargetLiveOnlyRow(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, "")
	r.ApplyLive("ollama", []Model{{ID: "only-live"}})
	ref, err := r.AliasTarget("ollama", "only-live")
	if err != nil {
		t.Fatal(err)
	}
	if ref != (Ref{Instance: "ollama", Model: "only-live"}) {
		t.Fatalf("AliasTarget(live-only) = %+v", ref)
	}
}
