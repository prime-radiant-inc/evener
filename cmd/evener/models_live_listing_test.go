package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gatewayModelsServer serves an OpenAI-compatible /models listing: one model
// with the given window, or a failing endpoint when window is 0.
func gatewayModelsServer(t *testing.T, window int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if window == 0 || r.URL.Path != "/v1/models" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "wide", "context_window": window}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

func gatewayConfig(t *testing.T, baseURL string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + baseURL + "\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
}

func inspectGateway(t *testing.T) map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"inspect", "gw/wide"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("inspect: %v (%s)", err, stderr.String())
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("inspect must print JSON: %v\n%s", err, stdout.String())
	}
	return out
}

// inspect shows what a session on the instance would budget against: the
// window its live listing reports, labelled as live and verified.
func TestModelsInspectAppliesLiveListing(t *testing.T) {
	modelsTestEnv(t)
	gatewayConfig(t, gatewayModelsServer(t, 524288))
	out := inspectGateway(t)
	caps := out["caps"].(map[string]any)
	if caps["context_window"] != float64(524288) {
		t.Fatalf("caps.context_window = %v, want the listed 524288", caps["context_window"])
	}
	if out["provenance"].(map[string]any)["ContextWindow"] != "live" {
		t.Fatalf("provenance = %v, want live", out["provenance"])
	}
	if out["live_listing"] != "applied" || out["context_window_verified"] != true {
		t.Fatalf("live_listing=%v context_window_verified=%v, want applied and verified", out["live_listing"], out["context_window_verified"])
	}
}

// With no listing to apply, the window is reported unverified instead of a
// catalog or placeholder figure standing in as fact.
func TestModelsInspectWithoutListingIsUnverified(t *testing.T) {
	modelsTestEnv(t)
	gatewayConfig(t, gatewayModelsServer(t, 0))
	out := inspectGateway(t)
	if _, ok := out["caps"].(map[string]any)["context_window"]; ok {
		t.Fatalf("an unlisted gateway model has no window: %v", out["caps"])
	}
	if out["live_listing"] != "unavailable" || out["context_window_verified"] != false {
		t.Fatalf("live_listing=%v context_window_verified=%v, want unavailable and unverified", out["live_listing"], out["context_window_verified"])
	}
}

// list on one instance applies the same listing and labels each row's window.
func TestModelsListInstanceAppliesLiveListing(t *testing.T) {
	modelsTestEnv(t)
	gatewayConfig(t, gatewayModelsServer(t, 524288))
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"list", "--provider", "gw"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("list: %v (%s)", err, stderr.String())
	}
	row := strings.Fields(listedRow(stdout.String(), "gw/wide"))
	if len(row) == 0 {
		t.Fatalf("live-only model missing from the listing:\n%s", stdout.String())
	}
	for _, want := range []string{"524288", "live", "yes"} {
		found := false
		for _, f := range row {
			found = found || f == want
		}
		if !found {
			t.Fatalf("row %v lacks %q (window, source, verified)", row, want)
		}
	}
}
