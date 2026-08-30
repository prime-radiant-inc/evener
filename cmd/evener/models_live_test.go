package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// liveSmokeModels names cheap models to exercise per instance. An instance
// with a credential that is not listed here uses the first ids its models
// endpoint returns.
var liveSmokeModels = map[string][]string{
	"openrouter":      {"google/gemini-2.5-flash-lite", "anthropic/claude-haiku-4.5", "deepseek/deepseek-v4-flash", "minimax/minimax-m2.7", "openai/gpt-4.1-nano"},
	"orclaude":        {"minimax/minimax-m2.7", "anthropic/claude-haiku-4.5"},
	"kimi-for-coding": {"kimi-for-coding", "k3"},
	"anthropic":       {"claude-haiku-4-5", "claude-sonnet-4-5[1m]"},
	"openai":          {"gpt-4.1-nano"},
	"groq":            {"openai/gpt-oss-120b"},
	"google":          {"gemini-2.5-flash-lite"},
	"deepseek":        {"deepseek-v4-flash"},
	"xai":             {"grok-4.3"},
	"mistral":         {"ministral-3b-latest"},
	"cerebras":        {"gpt-oss-120b"},
	"togetherai":      {"openai/gpt-oss-20b"},
	"moonshotai":      {"kimi-k2.5"},
	"zai":             {"glm-4.7-flash"},
	"minimax":         {"MiniMax-M2.7"},
}

const liveSmokePrompt = "Reply with the single word: pong"

// liveConfig names the providers.toml the smoke loads (its credentials.toml
// sibling supplies the keys). A flag rather than EVENER_PROVIDERS_CONFIG
// because TestMain scrubs every EVENER_* variable and redirects HOME.
var liveConfig = flag.String("live-config", "", "providers.toml for TestLiveSmoke (credentials.toml is its sibling)")

// TestLiveSmoke sends one minimal request per (instance, model) built from
// the registry's Resolved record — the resolved base URL, endpoint, auth
// scheme, headers, wire id, and body constants — against every instance the
// config has a credential for. It proves the registry's transport assembly
// against real endpoints before plan 2's builders exist. Run with:
//
//	EVENER_LIVE_TESTS=1 go test ./cmd/evener/ -run TestLiveSmoke -v -count=1 -args -live-config=/path/to/providers.toml
//
// It never prints a credential value: only URLs, header names, statuses,
// and short response excerpts.
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run the live provider smoke")
	}
	if *liveConfig == "" {
		t.Skip("pass -args -live-config=<providers.toml> to run the live provider smoke")
	}
	store, err := credentials.LoadStore(filepath.Join(filepath.Dir(*liveConfig), "credentials.toml"))
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	r, err := registry.Load(registry.WithConfigPath(*liveConfig), registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}),
		registry.WithStateRoot(t.TempDir()), registry.WithOffline(true))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Logf("%s; warnings=%v", r.UserLayerNote(), r.Warnings())
	client := &http.Client{Timeout: 90 * time.Second}
	ran := 0
	for _, inst := range r.Instances() {
		if inst.CredentialSource == "none" {
			t.Logf("skip %s: no credential", inst.Name)
			continue
		}
		switch inst.Auth {
		case registry.AuthBearer, registry.AuthOptionalBearer, registry.AuthHeader:
		default:
			t.Logf("skip %s: auth %s needs plan 2's authenticator", inst.Name, inst.Auth)
			continue
		}
		ran++
		t.Run(inst.Name, func(t *testing.T) { liveSmokeInstance(t, r, client, inst) })
	}
	if ran == 0 {
		t.Skip("no instance with a usable credential in this environment")
	}
}

func liveSmokeInstance(t *testing.T, r *registry.Registry, client *http.Client, inst registry.Instance) {
	t.Helper()
	models := liveSmokeModels[inst.Name]
	auto := false
	probeRef := inst.Name + "/probe"
	if len(models) > 0 {
		probeRef = inst.Name + "/" + models[0]
	}
	probe, err := r.Resolve(probeRef)
	if err != nil {
		t.Fatalf("resolve %s: %v", probeRef, err)
	}
	t.Logf("%s: protocol=%s auth=%s base=%s endpoint=%s models=%s credential=%s", inst.Name,
		probe.Protocol, probe.Transport.Auth, probe.Transport.BaseURL, probe.Transport.Endpoint, probe.Transport.ModelsEndpoint, probe.Credential.Source)

	// 1. The models endpoint, when the transport has one.
	if probe.Transport.ModelsEndpoint != registry.EndpointUnsupported {
		status, body := liveRequest(t, client, http.MethodGet, probe.Transport.BaseURL+probe.Transport.ModelsEndpoint, nil, probe)
		ids := liveModelIDs(body)
		if status/100 != 2 {
			t.Errorf("%s: GET %s → %d: %s", inst.Name, probe.Transport.ModelsEndpoint, status, excerpt(body, 200))
		} else {
			t.Logf("%s: GET %s → %d, %d ids", inst.Name, probe.Transport.ModelsEndpoint, status, len(ids))
			live := make([]registry.Model, 0, len(ids))
			for _, id := range ids {
				live = append(live, registry.Model{ID: id})
			}
			r.ApplyLive(inst.Name, live)
			if len(models) == 0 && len(ids) > 0 {
				// A listing says nothing about modality (a gateway may list
				// image models first), so auto-picked ids are tried in turn
				// and the instance passes when any of them answers.
				models = ids[:min(4, len(ids))]
				auto = true
			}
			// A listed id resolves through the live layer or a catalog row.
			if len(ids) > 0 {
				res, err := r.Resolve(inst.Name + "/" + ids[0])
				if err != nil {
					t.Errorf("%s: live id %q does not resolve: %v", inst.Name, ids[0], err)
				} else if m := res.Provenance["model"]; m != "live" && !strings.HasPrefix(m, "row:") && !strings.HasPrefix(m, "region:") && !strings.HasPrefix(m, "dated:") {
					t.Errorf("%s: live id %q resolved as %q, want live or a catalog row", inst.Name, ids[0], m)
				}
			}
		}
	}
	if len(models) == 0 {
		t.Logf("%s: no model to probe (no table entry and no listing)", inst.Name)
		return
	}

	// 2. One minimal completion per model, built from Resolved.
	answered := 0
	for _, model := range models {
		ref := inst.Name + "/" + model
		res, err := r.Resolve(ref)
		if err != nil {
			t.Errorf("resolve %s: %v", ref, err)
			continue
		}
		url, body, ok := liveBody(res)
		if !ok {
			t.Logf("%s: protocol %s not covered by the smoke", ref, res.Protocol)
			continue
		}
		status, resp := liveRequest(t, client, http.MethodPost, url, body, res)
		if status/100 != 2 {
			if auto {
				t.Logf("%s: POST %s (wire %s) → %d: %s", ref, res.Transport.Endpoint, res.WireID, status, excerpt(resp, 200))
			} else {
				t.Errorf("%s: POST %s (wire %s) → %d: %s", ref, res.Transport.Endpoint, res.WireID, status, excerpt(resp, 300))
			}
			continue
		}
		answered++
		t.Logf("%s: POST %s (wire %s) → %d, text=%q warnings=%v", ref, res.Transport.Endpoint, res.WireID, status, excerpt(liveText(res.Protocol, resp), 60), res.Warnings)
	}
	if auto && answered == 0 {
		t.Errorf("%s: none of the auto-picked models answered", inst.Name)
	}
}

// liveBody builds the smallest legal request per protocol from Resolved.
func liveBody(res registry.Resolved) (string, map[string]any, bool) {
	body := map[string]any{}
	switch res.Protocol {
	case registry.ProtocolOpenAIChat:
		field := "max_tokens"
		if res.Caps.MaxTokensField != nil && *res.Caps.MaxTokensField != "" {
			field = *res.Caps.MaxTokensField
		}
		body["model"] = res.WireID
		body["messages"] = []map[string]any{{"role": "user", "content": liveSmokePrompt}}
		body[field] = 64
	case registry.ProtocolOpenAIResponses:
		body["model"] = res.WireID
		body["input"] = liveSmokePrompt
		body["max_output_tokens"] = 64
	case registry.ProtocolAnthropic:
		body["model"] = res.WireID
		body["max_tokens"] = 64
		body["messages"] = []map[string]any{{"role": "user", "content": liveSmokePrompt}}
	case registry.ProtocolGoogle:
		body["contents"] = []map[string]any{{"role": "user", "parts": []map[string]any{{"text": liveSmokePrompt}}}}
		body["generationConfig"] = map[string]any{"maxOutputTokens": 64}
	default:
		return "", nil, false
	}
	for path, v := range res.Transport.Body {
		liveSetPath(body, path, v)
	}
	endpoint := strings.ReplaceAll(res.Transport.Endpoint, "{model}", res.WireID)
	if strings.Contains(res.Transport.Endpoint, "{model}") {
		delete(body, "model")
	}
	return res.Transport.BaseURL + endpoint, body, true
}

func liveSetPath(body map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	cur := body
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

// liveRequest performs one request with the resolved auth and headers and
// returns the status and body. It never logs a credential value.
func liveRequest(t *testing.T, client *http.Client, method, url string, body map[string]any, res registry.Resolved) (int, []byte) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	switch res.Transport.Auth {
	case registry.AuthBearer, registry.AuthOptionalBearer:
		if res.Credential.Value != "" {
			req.Header.Set("Authorization", "Bearer "+res.Credential.Value)
		}
	case registry.AuthHeader:
		req.Header.Set(res.Transport.AuthHeader, res.Credential.Value)
	}
	for k, v := range res.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range res.CredentialHeaders {
		req.Header.Set(k, v)
	}
	if res.Protocol == registry.ProtocolAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("%s %s: read: %v", method, url, err)
	}
	return resp.StatusCode, raw
}

// liveModelIDs reads the OpenAI-shaped {"data":[{"id":…}]} listing every
// protocol here uses (Anthropic's /models has the same shape).
func liveModelIDs(body []byte) []string {
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil
	}
	ids := make([]string, 0, len(listing.Data))
	for _, m := range listing.Data {
		if m.ID != "" && registry.IsChatModelID(m.ID) {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// liveText extracts the reply text per protocol for the log line.
func liveText(protocol string, body []byte) []byte {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	switch protocol {
	case registry.ProtocolOpenAIChat:
		if choices, ok := doc["choices"].([]any); ok && len(choices) > 0 {
			if msg, ok := choices[0].(map[string]any)["message"].(map[string]any); ok {
				return []byte(fmt.Sprint(msg["content"]))
			}
		}
	case registry.ProtocolOpenAIResponses:
		if out, ok := doc["output"].([]any); ok {
			for _, item := range out {
				if m, ok := item.(map[string]any); ok && m["type"] == "message" {
					if content, ok := m["content"].([]any); ok && len(content) > 0 {
						if c, ok := content[0].(map[string]any); ok {
							return []byte(fmt.Sprint(c["text"]))
						}
					}
				}
			}
		}
	case registry.ProtocolAnthropic:
		if content, ok := doc["content"].([]any); ok {
			for _, item := range content {
				if c, ok := item.(map[string]any); ok && c["type"] == "text" {
					return []byte(fmt.Sprint(c["text"]))
				}
			}
		}
	case registry.ProtocolGoogle:
		if cands, ok := doc["candidates"].([]any); ok && len(cands) > 0 {
			if content, ok := cands[0].(map[string]any)["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
					if p, ok := parts[0].(map[string]any); ok {
						return []byte(fmt.Sprint(p["text"]))
					}
				}
			}
		}
	}
	return body
}

func excerpt(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
