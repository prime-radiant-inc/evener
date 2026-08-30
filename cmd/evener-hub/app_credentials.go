package hub

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
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
	Models(context.Context, string) (llm.ModelListing, error)
	Close() error
}

type credentialProbeLoader func(string) (credentialProbeClient, error)

type credentialTestCall struct {
	done   chan struct{}
	result appwire.AuthTestResponse
}

// loadCredentialTestClient builds the probe client the way session startup
// does, against the same providers.toml the spawn path will read.
func loadCredentialTestClient(path string) (credentialProbeClient, error) {
	var (
		client *llm.Client
		err    error
	)
	if strings.TrimSpace(path) == "" {
		client, err = cmdutil.LoadClient("")
	} else {
		client, err = cmdutil.LoadClientAt(path, "")
	}
	if err != nil {
		// A typed nil in the interface would read as a usable client.
		return nil, err
	}
	return client, nil
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

// runCredentialTest asks the registry what the instance needs and whether it
// has it, then makes one harmless model-list call with the client the launch
// path would build. A providers.toml the registry read is never a
// configuration failure here (spec §11.3).
func (c *hubAuthController) runCredentialTest(ctx context.Context, name string, loader credentialProbeLoader) appwire.AuthTestResponse {
	r := c.registry()
	if r == nil {
		return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage)
	}
	inst, ok := r.Instance(name)
	if !ok {
		res, err := r.ResolveInstance(name)
		if err != nil {
			return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage)
		}
		inst = registry.Instance{Name: name, Auth: res.Transport.Auth, CredentialSource: res.Credential.Source}
	}
	required := inst.Auth != registry.AuthNone && inst.Auth != registry.AuthOptionalBearer
	if required && inst.CredentialSource == "none" {
		return credentialTestResponse(name, appwire.AuthTestStatusMissing, credentialTestMissingMessage)
	}
	client, err := loader(c.providersConfigPath)
	if err != nil || client == nil {
		return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage)
	}
	defer func() { _ = client.Close() }()

	probeCtx, cancel := context.WithTimeout(ctx, credentialTestTimeout)
	defer cancel()
	listing, err := client.Models(probeCtx, name)
	if err != nil {
		status, message := classifyCredentialTestError(err)
		return credentialTestResponse(name, status, message)
	}
	if !listing.Live {
		return credentialTestResponse(name, appwire.AuthTestStatusUnsupported, credentialTestUnsupportedMessage)
	}
	return credentialTestResponse(name, appwire.AuthTestStatusSuccess, credentialTestSuccessMessage)
}

func classifyCredentialTestError(err error) (string, string) {
	var configErr *llm.ConfigurationError
	if errors.As(err, &configErr) {
		return appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage
	}

	statusCode := 0
	if llmErr, ok := errors.AsType[llm.Error](err); ok {
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
