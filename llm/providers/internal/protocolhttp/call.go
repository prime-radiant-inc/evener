// Package protocolhttp is the HTTP plumbing shared by the protocol packages:
// it turns a built body and a Resolved record into a wire request in the
// spec §8.2 order (prune, body constants, authenticator, request preparer),
// executes it with API-attempt logging, and classifies failures. Protocol
// packages own only the body shape and the response decoding.
package protocolhttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	// BuildBody encodes a history-bearing operation for the request selected
	// for an HTTP recipient. Static operations may supply Body directly.
	BuildBody func(llm.Request) (map[string]any, error)
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
	// preparer, immediately before marshaling the body for the wire recipient.
	FinalizeBody func(recipient *http.Request, body map[string]any, view llm.Request) error
}

// Prepared is a Call after prune → constants → auth → prepare.
type Prepared struct {
	Request      *http.Request
	Body         []byte
	PrunedFields []string
	material     llm.APILogCredentialMaterial
	redirectBody func(*http.Request) error
}

// Prepare assembles the wire request without sending it (spec §8.2 steps
// 2–4): prune by Fields, apply Transport.Body constants, set the layered
// headers, run the authenticator, then the request preparer, and marshal
// the final body. Steps 2–4 all edit c.Body in place, so c.Body is the
// wire body afterwards, not the body the protocol built.
func Prepare(ctx context.Context, c *Call) (*Prepared, error) {
	view := c.Req
	bound := c.BuildBody != nil && view.HasReasoningEndpoints()
	if bound {
		view, _ = view.ForReasoningEndpoint(c.URL, c.Res)
	}
	body, pruned, err := c.bodyFor(view)
	if err != nil {
		return nil, err
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
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	auth := llm.AuthenticatorForContext(ctx, c.Res.Transport.Auth)
	if auth == nil {
		return nil, &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: no authenticator for auth scheme %q", c.Res.Instance, c.Res.Transport.Auth)}
	}
	if err := auth.Apply(ctx, httpReq, c.Res); err != nil {
		return nil, err
	}
	p := &Prepared{Request: httpReq, PrunedFields: pruned}
	if body != nil {
		// A redirected body's preparer sees the original authenticated
		// headers on a private copy. The redirect policy owns the headers
		// actually forwarded by net/http.
		var beforePreparer *http.Request
		if bound {
			beforePreparer = httpReq.Clone(ctx)
		}
		p.Body, err = c.marshalBody(httpReq, httpReq, auth, view, body)
		if err != nil {
			return nil, err
		}
		if bound {
			if err := p.bindReasoningEndpoints(c, beforePreparer, auth); err != nil {
				return nil, err
			}
		}
		setRequestBody(httpReq, p.Body)
	}
	p.material = credentialMaterial(c.Res, httpReq)
	return p, nil
}

func (p *Prepared) bindReasoningEndpoints(c *Call, authenticated *http.Request, auth llm.Authenticator) error {
	bodyAt := func(candidate *http.Request, recipient string) ([]byte, error) {
		view, _ := c.Req.ForReasoningEndpoint(recipient, c.Res)
		body, _, err := c.bodyFor(view)
		if err != nil {
			return nil, err
		}
		return c.marshalBody(authenticated.Clone(candidate.Context()), candidate, auth, view, body)
	}
	initial := *p.Request.URL
	initialRecipient := llm.ServingRecipient(initial.String(), c.Res)
	if initial.String() != c.URL {
		// A preparer or authenticator can change the constructed URL.
		b, err := bodyAt(p.Request, initialRecipient)
		if err != nil {
			return err
		}
		p.Body = b
	}
	atInitial := true
	p.redirectBody = func(candidate *http.Request) error {
		// net/http obtains every retained redirect body from the first
		// request's GetBody. It already has the first deployment's bytes.
		recipient := llm.ServingRecipient(candidate.URL.String(), c.Res)
		sameRecipient := recipient == initialRecipient
		if sameRecipient && atInitial {
			return nil
		}
		atInitial = sameRecipient
		b, err := bodyAt(candidate, recipient)
		if err != nil {
			return err
		}
		if sameRecipient {
			// The finalizers now describe this hop again. Keep the first
			// request's exact bytes and any caller body policy.
			return nil
		}
		_ = candidate.Body.Close()
		setRequestBody(candidate, b)
		return nil
	}
	return nil
}

func (c *Call) bodyFor(view llm.Request) (body map[string]any, pruned []string, err error) {
	body = c.Body
	if c.BuildBody != nil {
		body, err = c.BuildBody(view)
		if err != nil {
			return nil, nil, err
		}
	}
	if body != nil {
		pruned = registry.Prune(body, c.Res.Caps, c.Res.Protocol)
		registry.ApplyBodyConstants(body, c.Res.Transport.Body)
	}
	return body, pruned, nil
}

func (c *Call) marshalBody(httpReq, recipient *http.Request, auth llm.Authenticator, view llm.Request, body map[string]any) ([]byte, error) {
	if preparer, ok := auth.(llm.RequestPreparer); ok {
		if err := preparer.PrepareRequest(recipient.Context(), httpReq, body, view, c.Res); err != nil {
			return nil, err
		}
	}
	if c.FinalizeBody != nil {
		if err := c.FinalizeBody(recipient, body, view); err != nil {
			return nil, err
		}
	}
	return MarshalBody(body)
}

func setRequestBody(req *http.Request, b []byte) {
	req.Body = io.NopCloser(bytes.NewReader(b))
	req.ContentLength = int64(len(b))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

// URL joins the resolved base URL with an endpoint template, substituting
// {model} with the URL-escaped wire id (spec §9.1). Callers check for
// registry.EndpointUnsupported first.
func URL(res registry.Resolved, template string) string {
	return res.RequestURL(template)
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
// carries.
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

func (c *Call) httpClient(p *Prepared) *http.Client {
	var client *http.Client
	if c.Client != nil {
		client = c.Client
	} else {
		client = DefaultClient
	}
	clientCopy := *client
	checkRedirect := client.CheckRedirect
	credentialHeaders := make([]string, 0, len(c.Res.CredentialHeaders)+1)
	if c.Res.Transport.AuthHeader != "" {
		credentialHeaders = append(credentialHeaders, c.Res.Transport.AuthHeader)
	}
	for name := range c.Res.CredentialHeaders {
		credentialHeaders = append(credentialHeaders, name)
	}
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		retainedBody := req.Body != nil && req.Body != http.NoBody
		if checkRedirect != nil {
			if err := checkRedirect(req, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) > 0 && via[0].URL != nil && req.URL != nil && via[0].URL.Host != req.URL.Host {
			for _, name := range credentialHeaders {
				req.Header.Del(name)
			}
		}
		if p.redirectBody != nil && retainedBody && req.Body != nil && req.Body != http.NoBody && req.URL != nil {
			return p.redirectBody(req)
		}
		return nil
	}
	return &clientCopy
}

func (c *Call) metaBuilder(p *Prepared) transport.APIAttemptMetaBuilder {
	return func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		mode := c.Req.HistoryMode
		if c.Req.Continuation != nil {
			mode = c.Req.HistoryModeForEndpoint(wireRequest.URL.String(), c.Res)
		}
		return llm.APIAttemptMeta{
			ProviderInstance:   c.Res.Instance,
			RequestModel:       c.Req.Model,
			HistoryMode:        mode,
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
