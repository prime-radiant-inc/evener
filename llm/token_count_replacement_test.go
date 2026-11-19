package llm

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// A recorded baseline belongs to a catalog, not to a target.
//
// A caller may price a request for a strict Responses route, then replace the
// catalog with a smaller one and stamp the replacement, and dispatch it to a
// relaxed Chat route. The recorded emission is a count over the catalog the
// caller discarded; restoring it over the replacement's stamp prices a list this
// request no longer carries, so admission can reject a smaller catalog that fits
// or admit a larger one that exceeds the cap.
func TestAReplacementCatalogKeepsItsOwnStampAcrossTargets(t *testing.T) {
	t.Parallel()
	// The catalog the caller priced for the strict route.
	pricedCatalog := parameterlessCatalog(24)
	// The smaller replacement the caller sends instead, stamped the way a caller
	// that measured its own list stamps it.
	replacement := parameterlessCatalog(4)

	priced := priceEmittedToolSchemas(Request{
		Tools:          pricedCatalog,
		ToolsJSONBytes: ToolsJSONBytes(pricedCatalog),
	}, responsesRowOn(true))
	if priced.ToolsJSONBytes == ToolsJSONBytes(pricedCatalog) {
		t.Fatal("fixture does not distinguish the prices: the strict route left the count at the advertised one")
	}

	req := priced
	req.Tools = replacement
	req.ToolsJSONBytes = ToolsJSONBytes(replacement)
	// The relaxed Chat route's builder leaves this catalog as it is, so the
	// replacement's own stamp is the price.
	got := priceEmittedToolSchemas(req, chatRowOn(false))
	if got.ToolsJSONBytes != ToolsJSONBytes(replacement) {
		t.Fatalf("priced count = %d, want the replacement's own %d: the recorded baseline describes the catalog "+
			"the caller replaced, not the list this request carries",
			got.ToolsJSONBytes, ToolsJSONBytes(replacement))
	}
}

// A replacement the caller did not measure is measured by whoever prices it,
// rather than answered from another catalog's record.
func TestAnUnstampedReplacementCatalogIsMeasured(t *testing.T) {
	t.Parallel()
	pricedCatalog := parameterlessCatalog(24)
	priced := priceEmittedToolSchemas(Request{
		Tools:          pricedCatalog,
		ToolsJSONBytes: ToolsJSONBytes(pricedCatalog),
	}, responsesRowOn(true))

	replacement := parameterlessCatalog(4)
	req := priced
	req.Tools = replacement
	req.ToolsJSONBytes = 0

	got := priceEmittedToolSchemas(req, chatRowOn(false))
	if want := ToolsJSONBytes(replacement); got.ToolsJSONBytes != want {
		t.Fatalf("priced count = %d, want the replacement's own %d: an unstamped replacement is measured, not "+
			"answered from the catalog it replaced", got.ToolsJSONBytes, want)
	}
}

// A ProviderOptions tools value is the list those routes dispatch.
//
// The Chat Completions builder writes the request's tools and then copies the
// protocol's options over the body, so an explicit tools option replaces the
// projected catalog. Pricing the discarded catalog charges bytes the wire never
// carries: a cap between the advertised and projected counts can reject a
// request that sends a smaller replacement.
func TestAProviderToolsOptionIsTheListThatIsPriced(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		res   registry.Resolved
		small []ToolDefinition
	}{
		{"chat", chatRowOn(false), parameterlessCatalog(4)},
		{"responses", responsesRowOn(false), parameterlessCatalog(4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			catalog := parameterlessCatalog(24)
			// The replacement the caller puts on the wire: a smaller list of the
			// same definitions, which measures less than the catalog.
			encoded, err := json.Marshal(tc.small)
			if err != nil {
				t.Fatalf("measure the replacement: %v", err)
			}
			advertised := len(encoded)
			req := Request{
				Tools:          catalog,
				ToolsJSONBytes: ToolsJSONBytes(catalog),
				ProviderOptions: map[string]any{
					tc.res.Protocol: map[string]any{"tools": tc.small},
				},
			}
			got := priceEmittedToolSchemas(req, tc.res)
			if got.ToolsJSONBytes != advertised {
				t.Fatalf("priced count = %d, want the replacement's %d: the option replaces the catalog on this "+
					"route, so the discarded catalog is not what dispatches",
					got.ToolsJSONBytes, advertised)
			}
		})
	}
}

// The replacement is re-decided on every price: an option that is gone leaves the
// catalog's own count, not the price of a list the request no longer carries.
func TestAGoneToolsOptionRestoresTheCatalogPrice(t *testing.T) {
	t.Parallel()
	catalog := parameterlessCatalog(24)
	res := chatRowOn(false)
	req := Request{
		Tools:          catalog,
		ToolsJSONBytes: ToolsJSONBytes(catalog),
		ProviderOptions: map[string]any{
			res.Protocol: map[string]any{"tools": parameterlessCatalog(2)},
		},
	}
	withOption := priceEmittedToolSchemas(req, res)
	if withOption.ToolsJSONBytes == ToolsJSONBytes(catalog) {
		t.Fatal("fixture does not distinguish the prices: the option left the catalog's count")
	}

	withOption.ProviderOptions = nil
	got := priceEmittedToolSchemas(withOption, res)
	if want := ToolsJSONBytes(catalog); got.ToolsJSONBytes != want {
		t.Fatalf("priced count after the option was removed = %d, want the catalog's %d: the replacement's "+
			"price describes a list this request no longer carries", got.ToolsJSONBytes, want)
	}
}
