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
	// Registry is the configuration snapshot this client will dial.
	// runCredentialTest validates an asserted endpoint fingerprint against it -
	// not against the hub's own registry - because it is what the model-list
	// call resolves the name through - and binds that same registry's state
	// root to the request (see withScopedCodexAuth), so a client loaded from a
	// custom root reads that root's Codex record.
	Registry() *registry.Registry
}

type credentialProbeLoader func(path string, noUserLayer bool) (credentialProbeClient, error)

type credentialTestCall struct {
	done   chan struct{}
	result appwire.AuthTestResponse
	err    error
}

// credentialTestKey scopes the sharing of one in-flight probe to one asserted
// destination. Two callers checking the same name against the same endpoint
// share the dial; a caller asserting a different endpoint - or none - gets its
// own check, because a joined result would answer about a destination it never
// stated.
func credentialTestKey(name, asserted string) string {
	return name + "\x00" + asserted
}

// loadCredentialTestClient builds the probe client the child would get: the
// user layer at path while the hub is reading it, and none at all when there
// is no user layer or the file does not load (spec §10). Probing a client the
// child will never build answers about a different session.
func loadCredentialTestClient(path string, noUserLayer bool) (credentialProbeClient, error) {
	if noUserLayer {
		r, _, err := cmdutil.LoadRegistry(registry.WithNoUserLayer())
		if err != nil {
			return nil, err
		}
		return LiveRegistryClient(r), nil
	}
	var (
		client *llm.Client
		err    error
	)
	// Serialized like every other registry-client construction: the loaders
	// below wire process-wide tokenauth seams. An empty path means the
	// default user layer; anything else names the file to read.
	var opts []registry.Option
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		opts = append(opts, registry.WithConfigPath(trimmed))
	}
	var r *registry.Registry
	r, _, err = cmdutil.LoadRegistry(opts...)
	if err == nil && r != nil {
		client = LiveRegistryClient(r)
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
	asserted := strings.TrimSpace(params.ExpectedEndpointFingerprint)
	key := credentialTestKey(name, asserted)

	c.credentialTestMu.Lock()
	if c.credentialTests == nil {
		c.credentialTests = map[string]*credentialTestCall{}
	}
	if existing := c.credentialTests[key]; existing != nil {
		c.credentialTestMu.Unlock()
		if c.credentialTestJoined != nil {
			c.credentialTestJoined()
		}
		select {
		case <-existing.done:
			return existing.result, existing.err
		case <-ctx.Done():
			return credentialTestResponse(name, appwire.AuthTestStatusEndpointFailure, credentialTestEndpointMessage), nil
		}
	}
	loader := c.credentialTestLoader
	if loader == nil {
		loader = loadCredentialTestClient
	}
	call := &credentialTestCall{done: make(chan struct{})}
	c.credentialTests[key] = call
	c.credentialTestMu.Unlock()

	call.result, call.err = c.runCredentialTest(ctx, name, asserted, loader)
	c.credentialTestMu.Lock()
	if current := c.credentialTests[key]; current == call {
		delete(c.credentialTests, key)
		close(call.done)
	}
	c.credentialTestMu.Unlock()
	return call.result, call.err
}

// verifyProbeEndpoint refuses a check whose client asserted an endpoint the
// probe's own registry no longer resolves the name to. It reads the registry
// the probe client was built from, so the comparison and the model-list call
// see one configuration: the hub's own registry can lag a file-level
// reconfiguration, and a client that asserted the endpoint it was shown must
// not have its stored credential sent to a different one.
//
// An assertion the probe's registry cannot describe at all - the name does not
// resolve there, it is hidden, or the hub cannot key a fingerprint right now -
// is a refusal, not a bypass, exactly as verifyEndpointFingerprint treats one.
// An empty assertion is not checked here: the client captured no fingerprint to
// compare, which is what a client that was shown no endpoint sends.
func (c *hubAuthController) verifyProbeEndpoint(client credentialProbeClient, name, asserted string) error {
	r := client.Registry()
	if r == nil {
		return appwire.EndpointConflict(name + " cannot be checked: the probe cannot say where it would connect, so review its destination and run the check again")
	}
	inst, ok := r.Instance(name)
	if !ok {
		resolved, err := r.ResolveInstance(name)
		if err != nil {
			return appwire.EndpointConflict(name + " cannot be checked: it does not resolve on the configuration the check would use, so review its destination and run the check again")
		}
		inst = registry.Instance{Name: name, Auth: resolved.Transport.Auth, CredentialSource: resolved.Credential.Source}
	}
	current := destinationFingerprint(c.stateDir, r, inst)
	if current == "" {
		return appwire.EndpointConflict(name + " cannot be checked against the endpoint this check was started for: the probe cannot describe that destination, so review it and run the check again")
	}
	if current != asserted {
		return appwire.EndpointConflict(name + " no longer resolves to the endpoint this check was started for: review its destination and run the check again")
	}
	return nil
}

// runCredentialTest asks the registry what the instance needs and whether it
// has it, then makes one harmless model-list call with the client the launch
// path would build. A providers.toml the registry read is never a
// configuration failure here (spec §11.3).
func (c *hubAuthController) runCredentialTest(ctx context.Context, name, asserted string, loader credentialProbeLoader) (appwire.AuthTestResponse, error) {
	r := c.registry()
	if r == nil {
		return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage), nil
	}
	inst, ok := r.Instance(name)
	if !ok {
		res, err := r.ResolveInstance(name)
		if err != nil {
			return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage), nil
		}
		inst = registry.Instance{Name: name, Auth: res.Transport.Auth, CredentialSource: res.Credential.Source}
	}
	required := !keylessScheme(inst.Auth)
	if required && inst.CredentialSource == "none" {
		return credentialTestResponse(name, appwire.AuthTestStatusMissing, credentialTestMissingMessage), nil
	}
	client, err := loader(c.providersConfigPath, childNoUserLayer(c.noUserLayer, c.reg))
	if err != nil || client == nil {
		return credentialTestResponse(name, appwire.AuthTestStatusConfigurationFailure, credentialTestConfigurationMessage), nil
	}
	defer func() { _ = client.Close() }()

	// The probe dials what the loader just built, so that snapshot is what an
	// asserted endpoint fingerprint has to match: between the caller's listing
	// and this load the name can be re-pointed, and a check that ignored that
	// would send the stored credential to an endpoint the user never reviewed.
	if asserted != "" {
		if err := c.verifyProbeEndpoint(client, name, asserted); err != nil {
			return appwire.AuthTestResponse{}, err
		}
	}

	probeCtx, cancel := context.WithTimeout(withScopedCodexAuth(ctx, client.Registry()), credentialTestTimeout)
	defer cancel()
	listing, err := client.Models(probeCtx, name)
	if err != nil {
		status, message := classifyCredentialTestError(err)
		return credentialTestResponse(name, status, message), nil
	}
	if !listing.Live {
		return credentialTestResponse(name, appwire.AuthTestStatusUnsupported, credentialTestUnsupportedMessage), nil
	}
	return credentialTestResponse(name, appwire.AuthTestStatusSuccess, credentialTestSuccessMessage), nil
}

func classifyCredentialTestError(err error) (string, string) {
	if _, ok := errors.AsType[*llm.ConfigurationError](err); ok {
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
