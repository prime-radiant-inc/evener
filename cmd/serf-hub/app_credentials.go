package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

const credentialTestTimeout = 10 * time.Second

const (
	credentialTestSuccessMessage       = "Credentials verified."
	credentialTestMissingMessage       = "No credentials are configured for this instance. Add a key or sign in first."
	credentialTestAuthMessage          = "The provider rejected these credentials. Replace the key or sign in again."
	credentialTestEndpointMessage      = "The provider endpoint could not be reached. Check the endpoint and network connection."
	credentialTestConfigurationMessage = "Provider configuration could not be loaded. Check the instance settings."
	credentialTestUnsupportedMessage   = "This provider does not support harmless credential verification."
)

type credentialProbeClient interface {
	ListModels(context.Context, string) ([]llm.ModelInfo, error)
	Close() error
}

type credentialProbeLoader func(string) (credentialProbeClient, providercfg.Config, error)

type credentialTestCall struct {
	done   chan struct{}
	result appwire.AuthTestResponse
}

func loadCredentialTestClient(path string) (credentialProbeClient, providercfg.Config, error) {
	if strings.TrimSpace(path) == "" {
		client, cfg, _, err := cmdutil.LoadClient()
		return client, cfg, err
	}
	client, cfg, _, err := cmdutil.LoadClientAt(path)
	return client, cfg, err
}

// TestCredentials checks the effective credentials for one configured
// instance using the same client construction path as session startup. The
// response is deliberately limited to a fixed status and safe message.
func (c *hubAuthController) TestCredentials(ctx context.Context, params appwire.AuthTestParams) (appwire.AuthTestResponse, error) {
	name := normalizeAuthProvider(params.Provider)

	c.credentialTestMu.Lock()
	if c.credentialTests == nil {
		c.credentialTests = map[string]*credentialTestCall{}
	}
	if existing := c.credentialTests[name]; existing != nil {
		c.credentialTestMu.Unlock()
		if c.credentialTestJoined != nil {
			c.credentialTestJoined()
		}
		select {
		case <-existing.done:
			return existing.result, nil
		case <-ctx.Done():
			return credentialTestResponse(name, appwire.AuthTestStatusEndpointFailure, credentialTestEndpointMessage), nil
		}
	}
	loader := c.credentialTestLoader
	if loader == nil {
		loader = loadCredentialTestClient
	}
	call := &credentialTestCall{done: make(chan struct{})}
	c.credentialTests[name] = call
	c.credentialTestMu.Unlock()

	call.result = c.runCredentialTest(ctx, name, loader)
	c.credentialTestMu.Lock()
	if current := c.credentialTests[name]; current == call {
		delete(c.credentialTests, name)
		close(call.done)
	}
	c.credentialTestMu.Unlock()
	return call.result, nil
}

func (c *hubAuthController) runCredentialTest(ctx context.Context, name string, loader credentialProbeLoader) appwire.AuthTestResponse {
	client, cfg, err := loader(c.providersConfigPath)
	inst, ok := configuredInstance(cfg, name)
	if !ok {
		if err != nil {
			return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage)
		}
		return credentialTestResponse(name, appwire.AuthTestStatusMissing, credentialTestMissingMessage)
	}
	if credentialRequired(inst) && !c.instanceHasEffectiveCredential(name, inst) {
		return credentialTestResponse(name, appwire.AuthTestStatusMissing, credentialTestMissingMessage)
	}
	if err != nil || client == nil {
		return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage)
	}
	defer func() { _ = client.Close() }()

	probeCtx, cancel := context.WithTimeout(ctx, credentialTestTimeout)
	defer cancel()
	_, err = client.ListModels(probeCtx, name)
	if err != nil {
		status, message := classifyCredentialTestError(err)
		return credentialTestResponse(name, status, message)
	}
	return credentialTestResponse(name, appwire.AuthTestStatusSuccess, credentialTestSuccessMessage)
}

func configuredInstance(cfg providercfg.Config, name string) (providercfg.InstanceConfig, bool) {
	for _, inst := range cfg.Instances {
		if inst.Name == name {
			return inst, true
		}
	}
	return providercfg.InstanceConfig{}, false
}

// credentialRequired reports whether there is a credential to look for at all,
// which is the question serf/auth/test asks before it decides the instance is
// unconfigured. The envvars registry owns the auth-mode half of that answer —
// envvars.RequiresNoCredential is the predicate the launch preflight,
// credentials.Store.List and instanceStatus all ask — and it is keyed on the
// behavior tag, so an openai instance routed through chat-completions is judged
// as the openai-compatible provider it resolves as.
func credentialRequired(inst providercfg.InstanceConfig) bool {
	tag := providercfg.BehaviorTag(string(inst.Type), string(inst.APIStyle))
	if envvars.RequiresNoCredential(tag) {
		return false
	}
	if tag == "openai-compatible" && strings.TrimSpace(inst.BaseURL) != "" {
		return false
	}
	return true
}

// instanceHasEffectiveCredential reports whether the instance has a credential
// to test. The question is whether launch would find one, so the answer has to
// be drawn from where launch looks: the inline key and headers carried by the
// config, then credentials.Store.ResolveKey keyed by the credential tag — the
// same call cmdutil.LoadClient makes when it builds the client — and finally,
// for an instance that behaves as OpenAI proper, the stored OAuth record that
// has no key of its own to resolve. The two tags part company here: the key
// follows the endpoint the adapter contacts, while OAuth follows the behavior.
func (c *hubAuthController) instanceHasEffectiveCredential(name string, inst providercfg.InstanceConfig) bool {
	if apiKey, err := providercfg.ResolveAPIKey(inst.APIKey); err == nil && strings.TrimSpace(apiKey) != "" {
		return true
	}
	if hasResolvedCredentialHeader(inst.CredentialHeaders) {
		return true
	}
	if providercfg.CompatFamily(inst.Type, inst.APIStyle) && hasResolvedAuthorizationHeader(inst.Headers) {
		return true
	}
	credentialTag := providercfg.CredentialTag(string(inst.Type), string(inst.APIStyle), inst.BaseURL)
	if key, _ := c.creds.ResolveKey(name, credentialTag); strings.TrimSpace(key) != "" {
		return true
	}
	behaviorTag := providercfg.BehaviorTag(string(inst.Type), string(inst.APIStyle))
	if behaviorTag == "openai" {
		status, err := c.openAIInstanceStatus(name)
		return err == nil && status.SignedIn
	}
	return false
}

func hasResolvedCredentialHeader(headers map[string]string) bool {
	for name, raw := range headers {
		value, err := providercfg.ResolveHeaderValue(name, raw)
		if err == nil && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func hasResolvedAuthorizationHeader(headers map[string]string) bool {
	for name, raw := range headers {
		if !strings.EqualFold(name, "Authorization") {
			continue
		}
		value, err := providercfg.ResolveHeaderValue(name, raw)
		return err == nil && strings.TrimSpace(value) != ""
	}
	return false
}

func classifyCredentialTestError(err error) (string, string) {
	var configErr *llm.ConfigurationError
	if errors.As(err, &configErr) && strings.Contains(strings.ToLower(configErr.Message), "does not support listing models") {
		return appwire.AuthTestStatusUnsupported, credentialTestUnsupportedMessage
	}
	if errors.As(err, &configErr) {
		return appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage
	}

	statusCode := 0
	var llmErr llm.Error
	if errors.As(err, &llmErr) {
		statusCode = llmErr.StatusCode()
	}
	if statusCode == 0 {
		for _, code := range []int{401, 403} {
			if strings.Contains(err.Error(), "HTTP "+strconv.Itoa(code)) || strings.Contains(err.Error(), "status="+strconv.Itoa(code)) {
				statusCode = code
				break
			}
		}
	}
	if statusCode == 401 || statusCode == 403 || llm.Kind(err) == llm.KindAuthentication || llm.Kind(err) == llm.KindAccessDenied {
		return appwire.AuthTestStatusAuthRejected, credentialTestAuthMessage
	}
	return appwire.AuthTestStatusEndpointFailure, credentialTestEndpointMessage
}

func credentialTestResponse(provider, status, message string) appwire.AuthTestResponse {
	return appwire.AuthTestResponse{Provider: provider, Status: status, Message: message}
}
