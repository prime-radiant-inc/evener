// Package protocolhttp is the HTTP plumbing shared by the protocol packages:
// it turns a built body and a Resolved record into a wire request in the
// spec §8.2 order (prune, body constants, authenticator, request preparer),
// executes it with API-attempt logging, and classifies failures. Protocol
// packages own only the body shape and the response decoding.
package protocolhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

// DefaultClient serves protocols whose Client is nil.
var DefaultClient = &http.Client{}

// Call describes one HTTP exchange for a Resolved record.
type Call struct {
	// Operation labels the call in failure messages, e.g. "messages.create".
	Operation string
	// EndpointFamily is the apilog endpoint_family, e.g. "anthropic_messages".
	EndpointFamily string
	Method         string
	URL            string
	// Body is the built body; nil for GET. Prepare mutates it in place —
	// the prune deletes the fields the row turns off, the transport's body
	// constants are written into it, and a RequestPreparer may rename or
	// drop keys — so callers must not reuse a Body across calls or read it
	// back expecting the shape their builder produced.
	Body map[string]any
	// Headers are the protocol's fixed headers (anthropic-version, session
	// affinity); they are set after res.Headers so the protocol wins.
	Headers map[string]string
	Req     llm.Request
	Res     registry.Resolved
	// Client is nil for DefaultClient.
	Client *http.Client
	// Reclassify, when set, post-processes the classified error of a non-2xx
	// response (google's gRPC status remap); it receives the body and the
	// error ClassifyHTTPError produced.
	Reclassify func(status int, body []byte, err error) error
	// FinalizeBody, when set, runs after transport constants and the request
	// preparer, immediately before the final body is marshaled.
	FinalizeBody func(body map[string]any)
}

// Prepared is a Call after prune → constants → auth → prepare.
type Prepared struct {
	Request      *http.Request
	Body         []byte
	PrunedFields []string
	material     llm.APILogCredentialMaterial
}

// Prepare assembles the wire request without sending it (spec §8.2 steps
// 2–4): prune by Fields, apply Transport.Body constants, set the layered
// headers, run the authenticator, then the request preparer, and marshal
// the final body. Steps 2–4 all edit c.Body in place, so c.Body is the
// wire body afterwards, not the body the protocol built.
func Prepare(ctx context.Context, c *Call) (*Prepared, error) {
	var pruned []string
	if c.Body != nil {
		pruned = registry.Prune(c.Body, c.Res.Caps)
		registry.ApplyBodyConstants(c.Body, c.Res.Transport.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, c.Method, c.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.Res.Headers {
		httpReq.Header.Set(k, v)
	}
	for k, v := range c.Res.CredentialHeaders {
		httpReq.Header.Set(k, v)
	}
	for k, v := range c.Headers {
		httpReq.Header.Set(k, v)
	}
	if c.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	auth, ok := llm.AuthenticatorFor(c.Res.Transport.Auth)
	if !ok {
		return nil, &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: no authenticator for auth scheme %q", c.Res.Instance, c.Res.Transport.Auth)}
	}
	if err := auth.Apply(ctx, httpReq, c.Res); err != nil {
		return nil, err
	}
	if preparer, ok := auth.(llm.RequestPreparer); ok && c.Body != nil {
		if err := preparer.PrepareRequest(ctx, httpReq, c.Body, c.Req, c.Res); err != nil {
			return nil, err
		}
	}
	if c.FinalizeBody != nil && c.Body != nil {
		c.FinalizeBody(c.Body)
	}
	p := &Prepared{Request: httpReq, PrunedFields: pruned}
	if c.Body != nil {
		b, err := json.Marshal(c.Body)
		if err != nil {
			return nil, err
		}
		p.Body = b
		httpReq.Body = io.NopCloser(bytes.NewReader(b))
		httpReq.ContentLength = int64(len(b))
		httpReq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	}
	p.material = credentialMaterial(c.Res, httpReq)
	return p, nil
}

// URL joins the resolved base URL with an endpoint template, substituting
// {model} with the URL-escaped wire id (spec §9.1). Callers check for
// registry.EndpointUnsupported first.
func URL(res registry.Resolved, template string) string {
	path := strings.ReplaceAll(template, "{model}", url.PathEscape(res.WireID))
	return strings.TrimRight(res.Transport.BaseURL, "/") + path
}

// ModelInBody reports whether the completion path lacks {model}, in which
// case the body carries the wire id (spec §9.1).
func ModelInBody(res registry.Resolved) bool {
	return !strings.Contains(res.Transport.Endpoint, "{model}")
}

// authHeaderName is the header the scheme writes the credential to.
func authHeaderName(res registry.Resolved) string {
	if res.Transport.Auth == registry.AuthHeader && res.Transport.AuthHeader != "" {
		return res.Transport.AuthHeader
	}
	return "Authorization"
}

// credentialMaterial names every header that carries a credential and
// every value that must never reach a log: the resolved credential, the
// credential headers, the value the authenticator wrote (a Codex or ADC
// bearer token is not res.Credential.Value), and any userinfo the base URL
// carries, as llm.BuildAPILogCredentialMaterial does for the adapters.
func credentialMaterial(res registry.Resolved, httpReq *http.Request) llm.APILogCredentialMaterial {
	header := authHeaderName(res)
	names := []string{header}
	values := []string{res.Credential.Value}
	if httpReq != nil {
		if v := httpReq.Header.Get(header); v != "" {
			values = append(values, v, strings.TrimPrefix(v, "Bearer "))
		}
	}
	for name, value := range res.CredentialHeaders {
		names = append(names, name)
		values = append(values, value)
	}
	if httpReq != nil && httpReq.URL != nil && httpReq.URL.User != nil {
		values = append(values, httpReq.URL.User.Username())
		if password, ok := httpReq.URL.User.Password(); ok {
			values = append(values, password)
		}
	}
	return llm.NewAPILogCredentialMaterial(names, nil, values...)
}

func (c *Call) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return DefaultClient
}

func (c *Call) metaBuilder(p *Prepared) transport.APIAttemptMetaBuilder {
	return func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance:   c.Res.Instance,
			RequestModel:       c.Req.Model,
			HistoryMode:        c.Req.HistoryMode,
			EndpointFamily:     c.EndpointFamily,
			Protocol:           c.Res.Protocol,
			RequestBody:        requestBody,
			PrunedFields:       p.PrunedFields,
			CredentialMaterial: credentialMaterial(c.Res, wireRequest),
		}
	}
}

// classify turns a non-2xx response into the typed error, applying the
// call's Reclassify hook when present.
func (c *Call) classify(status int, headers http.Header, body []byte) error {
	err := llm.ClassifyHTTPError(c.Operation, status, headers, body, c.Res)
	if c.Reclassify != nil {
		err = c.Reclassify(status, body, err)
	}
	return err
}
