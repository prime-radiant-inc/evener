package registry

import "testing"

const listingConfig = `
[providers.p]
protocol = "openai-chat"
base_url = "https://x/v1"
[providers.pwithrow]
protocol = "openai-chat"
base_url = "https://x/v1"
default_model = "m"
[providers.pwithrow.models.m]
family = "mm"
`

// A provider without a default model (or with a glob one) lists through its
// own shape; the transport seam still merges the protocol's default endpoint
// paths into the raw head transport.
func TestResolveInstanceListingAndTransport(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, listingConfig)

	res, err := r.ResolveInstanceListing("p")
	if err != nil {
		t.Fatal(err)
	}
	if res.Instance != "p" || res.ModelID != "" {
		t.Fatalf("listing fallback shape = %+v", res)
	}

	transport, err := r.ResolveInstanceTransport("p")
	if err != nil {
		t.Fatal(err)
	}
	if transport.Protocol != ProtocolOpenAIChat {
		t.Fatalf("transport protocol = %q, want %q", transport.Protocol, ProtocolOpenAIChat)
	}
	if transport.Transport.Endpoint == "" {
		t.Fatal("the transport seam must merge the protocol's default endpoint paths")
	}

	if _, err := r.ResolveInstanceTransport("no-such"); err == nil {
		t.Fatal("an unknown instance must be refused")
	}

	// A default row the transport can serve is the launch destination; the
	// transport depth returns the model-less shape with the replayed row
	// riding along.
	rowRes, err := r.ResolveInstanceTransport("pwithrow")
	if err != nil {
		t.Fatal(err)
	}
	if rowRes.Model.ID != "m" {
		t.Fatalf("transport seam resolved row %q, want the default row m", rowRes.Model.ID)
	}
}
