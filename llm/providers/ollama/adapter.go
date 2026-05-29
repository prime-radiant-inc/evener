// Package ollama registers an "ollama" LLM provider that targets a local or
// remote Ollama server via its OpenAI-compatible Chat Completions endpoint.
//
// Resolution order for the base URL:
//  1. OLLAMA_BASE_URL — used as-is (must include /v1)
//  2. OLLAMA_HOST — Ollama's canonical env var; normalized to a /v1 URL
//  3. http://localhost:11434/v1 — default
//
// OLLAMA_API_KEY is optional and used only for authenticated proxies or
// Ollama Cloud. Local Ollama does not require a key.
//
// The factory always registers the adapter so explicit selection
// (--provider ollama) works zero-config. The adapter implements
// llm.NonDefaultEligible, which prevents it from becoming the silent
// default provider in environments where the user didn't intend it —
// the original concern that motivated the previous env-gate. Explicit
// addressing by name still works, so `serf --provider ollama` succeeds
// regardless of whether any OLLAMA_* env var is set.
package ollama

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "http://localhost:11434/v1"

const providerName = "ollama"

type adapter struct {
	name  string
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return providerName
}

// NonDefaultEligible marks the ollama adapter as ineligible for the
// client's auto-selected default provider. This adapter is always
// registered (so explicit --provider ollama works zero-config), but the
// silent default fallback should never land here.
func (a *adapter) NonDefaultEligible() {}

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := a.inner.Complete(ctx, req)
	resp.Provider = providerName
	return resp, llm.RewriteErrorProvider(err, providerName)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	inner, err := a.inner.Stream(ctx, req)
	if err != nil {
		return nil, llm.RewriteErrorProvider(err, providerName)
	}
	return newRewriteStream(inner), nil
}

func (a *adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	models, err := a.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].Provider = providerName
	}
	return models, nil
}

// rewriteStream wraps an inner llm.Stream and rewrites the Provider field on
// any embedded Response payloads so consumers see "ollama" instead of the
// inner adapter's "openai-compatible" stamp.
//
// Close() owns shutdown: it signals the pump to stop forwarding, closes the
// inner stream, and waits for the pump goroutine to exit before returning.
// This prevents a goroutine leak when a consumer calls Close() without
// draining Events() — without coordination, the pump would block forever
// trying to send into a full out-channel that nobody is reading.
type rewriteStream struct {
	inner   llm.Stream
	out     chan llm.StreamEvent
	done    chan struct{}
	pumpEnd chan struct{}
	once    sync.Once
}

func newRewriteStream(inner llm.Stream) *rewriteStream {
	s := &rewriteStream{
		inner:   inner,
		out:     make(chan llm.StreamEvent, 16),
		done:    make(chan struct{}),
		pumpEnd: make(chan struct{}),
	}
	go s.pump()
	return s
}

func (s *rewriteStream) pump() {
	defer close(s.pumpEnd)
	defer close(s.out)
	for {
		select {
		case <-s.done:
			return
		case ev, ok := <-s.inner.Events():
			if !ok {
				return
			}
			if ev.Response != nil {
				ev.Response.Provider = providerName
			}
			if ev.Err != nil {
				ev.Err = llm.RewriteErrorProvider(ev.Err, providerName)
			}
			select {
			case s.out <- ev:
			case <-s.done:
				return
			}
		}
	}
}

func (s *rewriteStream) Events() <-chan llm.StreamEvent { return s.out }

func (s *rewriteStream) Close() error {
	s.once.Do(func() { close(s.done) })
	err := s.inner.Close()
	<-s.pumpEnd
	return err
}

// resolveBaseURL implements the documented resolution order.
func resolveBaseURL(baseURLEnv, hostEnv string) string {
	if b := strings.TrimSpace(baseURLEnv); b != "" {
		return strings.TrimRight(b, "/")
	}
	if h := strings.TrimSpace(hostEnv); h != "" {
		return normalizeHost(h)
	}
	return defaultBaseURL
}

// normalizeHost converts an OLLAMA_HOST value (host, host:port, or full URL)
// into a complete base URL ending in /v1. IPv6 hosts are bracketed correctly:
// bare "::1" becomes "[::1]:11434", which a naive strings.Contains(":") check
// would have left as "::1" with the wrong scheme syntax. Values whose path
// already terminates in /v1 are preserved so paths like
// https://proxy.example/ollama/v1 are not double-suffixed.
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimRight(h, "/")
	if h == "" {
		return defaultBaseURL
	}
	if strings.Contains(h, "://") {
		// Has scheme — append /v1 if not already present.
		if strings.HasSuffix(h, "/v1") {
			return h
		}
		return h + "/v1"
	}
	// No scheme. Determine whether a port is present and whether the host
	// is a bare IPv6 literal that needs brackets.
	if _, _, err := net.SplitHostPort(h); err != nil {
		switch {
		case strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]"):
			// Bracketed IPv6 with no port.
			h = h + ":11434"
		case strings.Count(h, ":") >= 2:
			// Bare IPv6 with no port: "::1" or "fe80::1".
			h = "[" + h + "]:11434"
		default:
			// Hostname or IPv4 without a port.
			h = h + ":11434"
		}
	}
	return "http://" + h + "/v1"
}

// InstanceParams holds the configuration for a single ollama adapter instance.
type InstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
}

// NewForInstance constructs an ollama adapter from explicit parameters.
// Empty BaseURL falls back to the ollama default (http://localhost:11434/v1).
func NewForInstance(params InstanceParams) *adapter {
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	return &adapter{
		name: params.Name,
		inner: openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
			Name:    params.Name,
			BaseURL: base,
			APIKey:  params.APIKey,
		}),
	}
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		baseEnv := strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL"))
		hostEnv := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
		keyEnv := strings.TrimSpace(os.Getenv("OLLAMA_API_KEY"))
		// Always register: ollama implements NonDefaultEligible, so the
		// "silent default provider" concern is handled at the client
		// level. Explicit --provider ollama works zero-config.
		return &adapter{
			inner: &openaicompat.Adapter{
				APIKey:  keyEnv,
				BaseURL: resolveBaseURL(baseEnv, hostEnv),
				Client:  &http.Client{Timeout: 0},
			},
		}, true, nil
	})
	llm.RegisterInstanceAdapterFactory("ollama", "", func(inst providerconfig.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
		}), nil
	})
}
