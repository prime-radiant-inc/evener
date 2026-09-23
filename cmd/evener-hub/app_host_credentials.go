package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/internal/credentials"
)

// hubHostCredentialsPusher serves evener/host/pushCredentials (component 07c):
// it copies the controller's local provider-instance keys to one named remote
// host. It shares the host registry and the per-host client seam with the
// evener/host/request proxy (hubHostAdminController), so a remote-originated
// request is refused at the same shared dispatch guard (appsource's
// guardRemoteDispatch, reached through RemoteHubSource.resolveClient) before any
// host is dialed — there is no second guard.
//
// The push reads the controller's local credentials store and writes only the
// host's: the local store is never written, and no key is ever copied onto this
// controller. A key value reaches the wire only inside the conditional set's
// Value, never a report or an error.
type hubHostCredentialsPusher struct {
	admin *hubHostAdminController
	creds *credentials.Store
	// credsErr is why creds is nil, when it is: the store could not be read
	// (credentials.LoadStore, carried here from the auth controller's own
	// resolution). It is what the refusal below names, instead of claiming no
	// store was ever configured.
	credsErr error
}

// Push copies every local credentials-store entry to params.Host's own store and
// reports one result per entry.
//
// The unit of the push is the local credentials-store entry, whose key is the
// instance name (spec 07 "The instance→provider join rule"). That key is sent
// verbatim as the wire Provider value only to the two provider-keyed auth
// methods, evener/auth/status and evener/auth/apiKey/conditionalSet.
// evener/instance/list takes EmptyParams and has no provider parameter, so it is
// called once with {} and the returned entries are joined against the store
// keys by InstanceEntry.Name (an explicit instance) and
// AvailableProviders[].ID where that provider is Implicit (the implicit-provider
// fallback, the same condition the host's own resolution requires); the key is
// never passed to instance/list. A key with no counterpart is skipped as "no
// matching instance on the host" rather than pushed under a guessed provider.
// Every entry Names() returned gets exactly one result row, including one whose
// value was cleared before it could be read (a skip, not a failure).
//
// Per matched entry the status read captures ActiveSource and ConfigRevision,
// which are echoed into the conditional set's ExpectedSource/ExpectedRevision so
// the host refuses a credential that changed between this read and the write.
// The client never classifies from that read: the host's own locked conditional
// set makes the permit/skip decision and its Action is reported verbatim. A
// per-entry failure — a refused conditional set, a stale revision — is reported
// as "failed" with the reason and does not abort the remaining entries.
func (p *hubHostCredentialsPusher) Push(ctx context.Context, params appwire.HostPushCredentialsParams) (appwire.HostPushCredentialsResponse, error) {
	// Normalize the same way the proxy does: the host registry trims, the source
	// registry looks up verbatim.
	host := strings.TrimSpace(params.Host)
	if _, ok := p.admin.hosts.Get(host); !ok {
		return appwire.HostPushCredentialsResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", host))
	}
	remote, err := p.admin.remoteSourceFor(host)
	if err != nil {
		return appwire.HostPushCredentialsResponse{}, err
	}
	if p.creds == nil {
		reason := "credential push requires a local credentials store"
		if p.credsErr != nil {
			reason = "credential push requires a local credentials store, and this hub's could not be read: " + p.credsErr.Error()
		}
		return appwire.HostPushCredentialsResponse{}, appwire.InternalError(reason)
	}

	// Read the local store first: the entry keys are the instance names. Sorted
	// so the report is deterministic regardless of the file's own key order.
	names := p.creds.Names()
	sort.Strings(names)

	// evener/instance/list is the authority for "present on the host". It takes
	// EmptyParams and is called exactly once; the join is on the returned
	// entries, never an instance name passed as a parameter.
	var listRaw json.RawMessage
	if err := p.forward(ctx, remote, appwire.MethodEvenerInstanceList, appwire.EmptyParams{}, &listRaw); err != nil {
		// A failed listing is a whole-call failure, not a per-entry one: without
		// it there is no authority for which keys have a counterpart.
		return appwire.HostPushCredentialsResponse{}, err
	}
	var listing appwire.InstanceListResponse
	if err := json.Unmarshal(listRaw, &listing); err != nil {
		return appwire.HostPushCredentialsResponse{}, appwire.InternalError("decode the host's instance list: " + err.Error())
	}
	// A host that cannot load its own providers.toml refuses its writes and
	// answers with an empty or partial instance list (InstanceListResponse
	// .WritesRefused; the host's own mutators refuse through
	// hubInstancesController.refuseWhenBroken). This call's only authority for
	// "present on the host" is that list, so pushing anyway would join the local
	// keys against a list that describes nothing and report every one of them
	// "skipped: no matching instance on the host" - a completed push reported
	// against a host that could not read its own instances, which is the class
	// of lie this whole report exists to avoid. The refusal ends the call, and
	// the host's own diagnostics are the reason it carries.
	if listing.WritesRefused {
		diagnostics := strings.Join(listing.Diagnostics, "; ")
		if diagnostics == "" {
			diagnostics = "the host reported no diagnostics"
		}
		return appwire.HostPushCredentialsResponse{}, appwire.InternalError(fmt.Sprintf("no credential can be pushed to %q: it cannot read its own providers.toml, so it refuses writes (%s)", host, diagnostics))
	}
	// The join is the spec's instance->provider rule, both halves of it: an
	// explicit instance matches by its name, and the implicit-provider fallback
	// matches by the provider ID - but only for a provider the host itself
	// resolves that fallback for, which is `Provider.Implicit`
	// (AvailableProviders[].Implicit / registry.BoolValue(p.Implicit),
	// llm/registry/types.go:247). That is the same condition the host's own
	// resolution gates the fallback on (hubAuthController.statusLocked,
	// instanceAuthScheme and endpointInstanceFor all require Implicit before
	// consulting registry.Provider). A provider ID that is not implicit is an
	// authoring/curation target, not an instance the host can take a key for, so
	// matching it would dispatch a local secret for a name the host answers
	// "not a configured provider or instance" to - the local key must be skipped
	// as "no matching instance on the host" instead.
	//
	// ProviderDescriptor.Setup is deliberately not the test: its own doc says it
	// is "safe discovery metadata for an addressable implicit provider or
	// existing instance" (appwire/types.go), so it is set for non-implicit
	// provider IDs too (an instance addressing that ID), which the Instances
	// name match already covers. Implicit is the fallback marker; addressability
	// is the host's answer to resolve separately.
	present := make(map[string]bool, len(listing.Instances)+len(listing.AvailableProviders))
	for _, inst := range listing.Instances {
		present[inst.Name] = true
	}
	for _, provider := range listing.AvailableProviders {
		if !provider.Implicit {
			continue
		}
		present[provider.ID] = true
	}

	response := appwire.HostPushCredentialsResponse{Host: host, Results: make([]appwire.HostCredentialPushResult, 0, len(names))}
	for _, name := range names {
		value, ok := p.creds.Get(name)
		if !ok {
			// Names() listed it but the value is gone: the entry was cleared or
			// blanked on the controller between the listing and this read. Nothing
			// failed - there was simply no key to push - so it is a skip, and it
			// still gets a row so the report accounts for every entry Names()
			// returned.
			response.Results = append(response.Results, appwire.HostCredentialPushResult{
				Instance: name,
				Action:   appwire.HostCredentialPushSkipped,
				Reason:   "the local key was cleared before it could be pushed",
			})
			continue
		}
		if !present[name] {
			response.Results = append(response.Results, appwire.HostCredentialPushResult{
				Instance: name,
				Action:   appwire.HostCredentialPushSkipped,
				Reason:   "no matching instance on the host",
			})
			continue
		}
		response.Results = append(response.Results, p.pushOne(ctx, remote, name, value))
	}
	return response, nil
}

// pushOne pushes one local entry: it reads the host's status to capture the
// source/revision to fence on, then calls the host's conditional set. Any
// failure is one entry's "failed" result; it never aborts the caller's loop.
func (p *hubHostCredentialsPusher) pushOne(ctx context.Context, remote *appsource.RemoteHubSource, name, value string) appwire.HostCredentialPushResult {
	failed := func(reason string) appwire.HostCredentialPushResult {
		return appwire.HostCredentialPushResult{Instance: name, Action: appwire.HostCredentialPushFailed, Reason: reason}
	}

	var statusRaw json.RawMessage
	if err := p.forward(ctx, remote, appwire.MethodEvenerAuthStatus, appwire.AuthStatusParams{Provider: name}, &statusRaw); err != nil {
		return failed(err.Error())
	}
	var status appwire.AuthStatusResponse
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		return failed("decode the host's auth status: " + err.Error())
	}

	// The read supplies the fence only; the host re-resolves the source and the
	// revision under its credential write lock and classifies there.
	var setRaw json.RawMessage
	if err := p.forward(ctx, remote, appwire.MethodEvenerAuthApiKeyConditionalSet, appwire.ApiKeyConditionalSetParams{
		Provider:         name,
		Value:            value,
		ExpectedSource:   status.ActiveSource,
		ExpectedRevision: status.ConfigRevision,
	}, &setRaw); err != nil {
		return failed(err.Error())
	}
	var set appwire.ApiKeyConditionalSetResponse
	if err := json.Unmarshal(setRaw, &set); err != nil {
		return failed("decode the host's conditional set: " + err.Error())
	}
	// The host's action is otherwise reported as it stands, unknown names
	// included: an action this controller has never heard of is a newer host's
	// own decision, and folding it into "failed" or relabelling it would hide
	// what the host actually did. An empty or whitespace-only action is not an
	// unknown action, though - it is no action at all, and a report row whose
	// Action is outside the documented added/updated/skipped/failed contract
	// tells the pane nothing about whether the key landed. That one is a failed
	// entry naming the malformed answer.
	if strings.TrimSpace(set.Action) == "" {
		return failed("the host answered evener/auth/apiKey/conditionalSet without an action, so whether the key landed is unknown")
	}
	return appwire.HostCredentialPushResult{Instance: name, Action: set.Action, Reason: set.Reason}
}

// forward sends one push call through the same per-host client seam and the same
// read-vs-mutation dispatch evener/host/request uses: the proxy's own mutation
// table decides, so the shared origin guard covers the push without a second
// guard and a read can never ride the outcome-unknown mapping (nor the
// conditional set the retryable read mapping) by being named at a call site.
func (p *hubHostCredentialsPusher) forward(ctx context.Context, remote *appsource.RemoteHubSource, method string, params any, out *json.RawMessage) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return appwire.InternalError(fmt.Sprintf("encode %s params: %v", method, err))
	}
	if _, mutating := remoteHostAdminMutationMethods[method]; mutating {
		return remote.AdminMutationCall(ctx, method, raw, out)
	}
	return remote.AdminCall(ctx, method, raw, out)
}
