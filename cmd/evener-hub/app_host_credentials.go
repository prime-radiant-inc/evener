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
	"primeradiant.com/evener/llm/registry"
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
// One entry never reaches the wire as a Value, decided here rather than by the
// host: one whose value is not an API key at all (the store also holds Google
// credential JSON, see credentialPushValueIsKey). It is a skip in the report.
// Every other matched entry is sent: the host's locked conditional set is the
// authority on what a key may do - including on a scheme that reads no key,
// which comes back as the host's own typed "skipped" - so the controller never
// judges a scheme's capability from the instance-list snapshot. The accepted
// cost of that is a key reaching an instance whose current scheme reads none,
// where the host skips it after the value has left this controller; the loop
// below states the tradeoff and why the alternative was rejected.
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
	//
	// matched carries, per key that has a counterpart on the host, the host's own
	// spelling of the entry, which is what travels as the wire Provider (the host
	// resolves that value). The scheme the entry authenticates with is deliberately
	// NOT read here: the host's locked conditional set is the authority on what a
	// key may do, and this listing is a snapshot a concurrent provider edit can
	// outdate, so a scheme judged here could skip a write the host would have
	// made.
	//
	// The key is case-folded, and only for the lookup: the local store lowercases
	// the keys it holds (Store.Set/Get), while the host spells its instances as
	// its own providers.toml authors them, so an exact lookup would call a
	// mixed-case host instance "no matching instance" without ever asking about
	// it. The host's own spelling is what travels as the wire Provider - the host
	// is the side that resolves that value - while the report's row stays the
	// local store key it accounts for.
	type hostEntry struct {
		name string
	}
	matched := make(map[string]hostEntry, len(listing.Instances)+len(listing.AvailableProviders))
	for _, inst := range listing.Instances {
		matched[strings.ToLower(inst.Name)] = hostEntry{name: inst.Name}
	}
	for _, provider := range listing.AvailableProviders {
		if !provider.Implicit {
			continue
		}
		// An explicit instance row wins over the provider descriptor for the name:
		// the row is the more specific answer, and its spelling is the one the
		// host's own listing uses for that instance.
		if key := strings.ToLower(provider.ID); matched[key].name == "" {
			matched[key] = hostEntry{name: provider.ID}
		}
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
		entry, ok := matched[strings.ToLower(name)]
		if !ok {
			response.Results = append(response.Results, appwire.HostCredentialPushResult{
				Instance: name,
				Action:   appwire.HostCredentialPushSkipped,
				Reason:   "no matching instance on the host",
			})
			continue
		}
		// The value leaves this hub only if it is an API key. The store holds
		// Google credential JSON under the same instance-name namespace
		// (evener/auth/credentialJson/set), and nothing but a key may be sent as
		// a Value: sent as one, a credential JSON would be stored on the host as
		// an api key its registry never reads, and the bytes would have left this
		// controller either way.
		if !credentialPushValueIsKey(value) {
			response.Results = append(response.Results, appwire.HostCredentialPushResult{
				Instance: name,
				Action:   appwire.HostCredentialPushSkipped,
				Reason:   "the stored value is shaped like a JSON document (it begins with a brace or a bracket) and no API key begins with either, so it cannot be told apart from a credential document: it is not sent to another host as a key, because a credential document's private material must never leave this controller",
			})
			continue
		}
		// The scheme is deliberately not consulted here. Classification belongs to
		// the host's conditional set, which runs under its own lock against the
		// current configuration; a decision here would be made from the
		// instance/list snapshot above, and that reading is stale by construction -
		// an entry that was gcp-adc when it was taken can be key-capable by the time
		// the push lands, so a controller-side skip would silently refuse a write
		// the host would have made.
		//
		// The cost of leaving it to the host is that a key can reach an instance
		// whose CURRENT scheme reads none: the host answers "skipped" with its own
		// reason, but the value it will not use has already left this controller.
		// That is accepted, because the host is the authority on what a key may do
		// and because the other choice is wrong in the other direction too -
		// refusing a valid push from a snapshot that no longer describes the host.
		// The value-kind skip above is a different question and stays: a credential
		// DOCUMENT must never leave this controller whatever the host's current
		// scheme is, which is not a staleness question at all.
		response.Results = append(response.Results, p.pushOne(ctx, remote, name, entry.name, value))
	}
	return response, nil
}

// credentialPushActionKnown reports whether action is one of the four the push
// report's contract names (appwire.HostCredentialPush*): the values the pane
// renders and the only ones a result row may carry. Compared exactly, because
// the contract is four exact strings - a padded or otherwise altered action is
// not one of them, and says so in the failed entry's reason.
func credentialPushActionKnown(action string) bool {
	switch action {
	case appwire.HostCredentialPushAdded, appwire.HostCredentialPushUpdated,
		appwire.HostCredentialPushSkipped, appwire.HostCredentialPushFailed:
		return true
	}
	return false
}

// credentialPushValueIsKey reports whether a credentials-store entry may be sent
// as a Value. The store holds two kinds of secret under one namespace of
// instance names: API keys (evener/auth/apiKey/set) and Google credential JSON
// (evener/auth/credentialJson/set), and only the first is this push's unit.
//
// Two rules, both fail-closed (an unproven value is not sent):
//
//   - A value the registry's own gate accepts as a Google credential JSON is
//     not a key. That gate is registry.CheckCredentialJSON, the predicate the
//     gcp-adc resolution path runs over a store entry, so the two agree on what
//     a credential document is.
//   - A trimmed value whose first character is "{" or "[" is not a key either,
//     whether or not its body parses. The SHAPE is the guard rather than
//     json.Valid: a TRUNCATED document - {"type":"service_account", - fails
//     the gate above and json.Valid alike, so a validity test answers "this is a
//     key" for exactly the pasted-credential case this rule exists to catch, and
//     the push would hand a service-account key to another host as an API key.
//     No api key begins with either character - they are opaque tokens - while
//     every JSON document does, so the shape alone decides, and an unparseable
//     body is still a pasted credential whose private material must not be
//     copied to another host.
//
// The shape rule is a heuristic, and it is deliberately kept one. A legitimate
// key whose first character happens to be "{" or "[" is refused along with a
// credential document, because nothing available here can tell the two apart:
// this function receives only the stored value, credentials.Store carries no
// kind or provenance (Get returns a bare string), and the registry's own gate
// parses content rather than knowing a type. The report's reason says what was
// observed - the value's shape - rather than claiming the entry holds a
// document, which the controller cannot prove for such a key. Preserving the
// credential kind in the store (a field, or a sibling record written by
// evener/auth/credentialJson/set) is the change that would lift the limitation;
// until then the rule fails closed, because the two mistakes are not
// symmetric - refusing a key costs an operator a visible skip they can redo,
// while sending credential material to another host cannot be taken back.
func credentialPushValueIsKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	if registry.CheckCredentialJSON([]byte(trimmed)) == nil {
		return false
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return false
	}
	return true
}

// pushOne pushes one local entry to the host's entry for it: name is the local
// store key this result accounts for, and remoteName is the host's own spelling
// of the instance, which is what travels as the wire Provider (the host resolves
// that value, so its spelling is the one to send). Any failure is one entry's
// "failed" result; it never aborts the caller's loop.
func (p *hubHostCredentialsPusher) pushOne(ctx context.Context, remote *appsource.RemoteHubSource, name, remoteName, value string) appwire.HostCredentialPushResult {
	failed := func(reason string) appwire.HostCredentialPushResult {
		return appwire.HostCredentialPushResult{Instance: name, Action: appwire.HostCredentialPushFailed, Reason: reason}
	}

	var statusRaw json.RawMessage
	if err := p.forward(ctx, remote, appwire.MethodEvenerAuthStatus, appwire.AuthStatusParams{Provider: remoteName}, &statusRaw); err != nil {
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
		Provider:         remoteName,
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
	// The report's contract is added|updated|skipped|failed, and the pane keys on
	// those four names: an action outside it - from a newer host, or from one
	// that is not the hub it claims to be - must not reach the report as a fifth,
	// so an unrecognized action is a failed entry. The host's own text is carried
	// in the reason verbatim rather than dropped: the operator still reads what
	// the host answered, which is what passing it through was protecting.
	//
	// An empty or whitespace-only action is the malformed case, which is one
	// step further: there is no action to quote.
	if strings.TrimSpace(set.Action) == "" {
		return failed("the host answered evener/auth/apiKey/conditionalSet without an action, so whether the key landed is unknown")
	}
	if !credentialPushActionKnown(set.Action) {
		return failed(fmt.Sprintf("the host answered evener/auth/apiKey/conditionalSet with an action the report's contract does not define (%q), so whether the key landed is unknown", set.Action))
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
