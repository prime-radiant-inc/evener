package hub

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm/registry"
)

// hubInstancesController manages provider instance CRUD: Create, Edit,
// Remove, SetDefault, and List. It is the only writer of providers.toml
// (spec §11.3): every read comes from the registry, every write goes through
// the registry's config writer and is followed by a reload, and a file the
// registry could not parse is never rewritten.
type hubInstancesController struct {
	reg                 *hubcore.ProviderRegistry
	providersConfigPath string
	auth                *hubAuthController
	mu                  sync.Mutex
}

func (c *hubInstancesController) read() (*registry.Layer, bool, error) {
	return registry.ReadConfigFile(c.providersConfigPath)
}

func (c *hubInstancesController) write(l *registry.Layer) error {
	return registry.WriteConfigFile(c.providersConfigPath, l)
}

// List returns every instance the registry currently holds, each with its
// credential status, plus the providers an add form can build on and the
// diagnostics the pane shows above them.
func (c *hubInstancesController) List() appwire.InstanceListResponse {
	entries := make([]appwire.InstanceEntry, 0)
	providers := make([]appwire.ProviderDescriptor, 0)
	userLayer := ""
	r := c.reg.Get()
	if r != nil {
		// The authored layer is what the sheet's form edits, so the entry
		// carries the authored credential fields alongside the registry's
		// resolved view. A file that cannot be read right now simply
		// prefills nothing; the refusal itself is already in Diagnostics.
		var layer *registry.Layer
		if l, exists, err := c.read(); err == nil && exists {
			layer = l
		}
		for _, inst := range r.Instances() {
			var authored *registry.Provider
			if layer != nil {
				if p, ok := layer.Providers[inst.Name]; ok {
					authored = &p
				}
			}
			entries = append(entries, c.entryFor(inst, authored))
		}
		for _, id := range r.ProviderIDs() {
			p, ok := r.Provider(id)
			if !ok {
				continue
			}
			// Only the variables a URL template reads become inputs; the
			// rest of vars_env (a credential's own variable) has no
			// instance-level meaning the add form could give it.
			vars := r.TemplateVarsEnv(id)
			var setup *appwire.InstanceEntry
			inst, addressable := r.Instance(id)
			if !addressable {
				if resolved, err := r.ResolveInstance(id); err == nil {
					// Resolve also addresses implicit providers with no credential
					// or incomplete destination. Copy listing metadata only, never
					// the resolved credential value or either headers map.
					inst = registry.Instance{
						Name: resolved.Instance, ProviderID: resolved.ProviderID,
						Protocol: resolved.Protocol, Surface: resolved.Surface,
						Auth: resolved.Transport.Auth, Implicit: true, Hidden: p.Hidden,
						CredentialSource: resolved.Credential.Source,
						ShadowedEnvVar:   resolved.ShadowedEnvVar, Warnings: resolved.Warnings,
					}
					if !p.Hidden {
						inst.BaseURL = resolved.Transport.BaseURL
					}
					addressable = true
				}
			}
			if addressable {
				var authored *registry.Provider
				if layer != nil {
					if p, ok := layer.Providers[id]; ok {
						authored = &p
					}
				}
				entry := c.entryFor(inst, authored)
				setup = &entry
			}
			providers = append(providers, appwire.ProviderDescriptor{
				ID:        id,
				Name:      p.Name,
				Protocol:  p.Protocol,
				Auth:      p.Transport.Auth,
				VarsEnv:   slices.Sorted(maps.Values(vars)),
				Vars:      vars,
				APIKeyEnv: append([]string(nil), p.APIKeyEnv...),
				Implicit:  registry.BoolValue(p.Implicit),
				AuthModes: authModesFor(p.Transport.Auth),
				Setup:     setup,
			})
		}
		userLayer = r.UserLayerNote()
	}
	return appwire.InstanceListResponse{
		Instances:          entries,
		AvailableProviders: providers,
		Diagnostics:        c.reg.Diagnostics(),
		UserLayer:          userLayer,
		// The wire bit is the refusal the mutators would give, asked once, so
		// the pane cannot offer an edit this controller would reject.
		WritesRefused: c.refuseWhenBroken() != nil,
	}
}

// entryFor is the wire view of one instance: the registry's own description,
// plus the credential status the auth controller derives for it, and the
// credential fields from its authored entry — nil for an implicit instance,
// which has no entry in providers.toml and so prefills neither.
func (c *hubInstancesController) entryFor(inst registry.Instance, authored *registry.Provider) appwire.InstanceEntry {
	status := c.auth.instanceStatus(inst)
	entry := appwire.InstanceEntry{
		Name:               inst.Name,
		Base:               inst.Base,
		ProviderID:         inst.ProviderID,
		Protocol:           inst.Protocol,
		Surface:            inst.Surface,
		Auth:               inst.Auth,
		BaseURL:            sanitizeEndpointURL(inst.BaseURL),
		Vars:               inst.Vars,
		Implicit:           inst.Implicit,
		Hidden:             inst.Hidden,
		IsDefault:          inst.Default,
		AuthModes:          status.AuthModes,
		ActiveSource:       status.ActiveSource,
		HasStoredFile:      status.HasStoredFile,
		HasStoredOAuth:     status.HasStoredOAuth,
		EnvVar:             status.EnvVar,
		ShadowedEnvVar:     status.ShadowedEnvVar,
		StoredEmail:        status.StoredEmail,
		CredentialRequired: inst.Auth != registry.AuthNone && inst.Auth != registry.AuthOptionalBearer,
		Warnings:           inst.Warnings,
	}
	if authored != nil {
		// api_key_env names an environment variable, and the loader takes
		// whatever string the TOML grammar spells, so a key pasted into that
		// field loads. It is omitted rather than sent, exactly as
		// credentialHeaderField omits a header the authoring rule refuses.
		if len(authored.APIKeyEnv) > 0 && registry.CheckAPIKeyEnvName(authored.APIKeyEnv[0]) == nil {
			entry.APIKeyEnv = authored.APIKeyEnv[0]
		}
		entry.CredentialHeader = credentialHeaderField(authored.CredentialHeaders)
	}
	return entry
}

// credentialHeaderField renders the authored credential_headers map as the
// single NAME=VALUE field the forms use; credentialHeaderFrom is its
// inverse. Several headers are possible by hand-editing the file, never
// through the pane; the first in sorted order is the one the form edits, and
// editing that field replaces the whole map.
//
// A name or a value the authoring rule would refuse is omitted rather than
// sent. registry.CheckCredentialHeaderName and CheckCredentialHeaderValue
// guard evener's own authoring surfaces only — the loader's checkEnvRefs
// passes any value without a '$' and reads any name the TOML grammar spells
// — so a hand-written literal secret or a name carrying a CR/LF loads fine,
// and neither must reach a client. Prefilling one would also build a form
// Edit refuses to save.
func credentialHeaderField(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	names := slices.Sorted(maps.Keys(headers))
	value := headers[names[0]]
	if registry.CheckCredentialHeaderName(names[0]) != nil || registry.CheckCredentialHeaderValue(value) != nil {
		return ""
	}
	return names[0] + "=" + value
}

// sanitizeEndpointURL keeps only the non-secret endpoint identity exposed to
// clients. Runtime requests continue to use the authored BaseURL; this copy is
// only for instance-list UI metadata and must not carry userinfo, query tokens,
// or fragments across the appwire boundary.
func sanitizeEndpointURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// writeLoadable is the invariant every mutation holds: a providers.toml the
// hub writes must be one the registry can read back. registry.WriteConfigFile
// re-parses what it marshals, so every rule the parser enforces — the
// protocol and surface vocabularies, the $VAR syntax in credential headers
// and api_key, unknown keys — refuses the write instead of landing on disk.
// Without that the write succeeds, the reload that follows fails,
// refuseWhenBroken flips, and the corrective edit is refused too: the pane
// locked out of its own recovery.
//
// Only that refusal is about the fields the caller sent, so only it comes
// back as invalid params; the parser never echoes a value it rejects, so its
// error is safe to return. A filesystem failure is the hub's problem, not the
// caller's, and is returned as it came.
func (c *hubInstancesController) writeLoadable(l *registry.Layer) error {
	err := c.write(l)
	if errors.Is(err, registry.ErrConfigUnloadable) {
		return appwire.InvalidParams(err.Error())
	}
	return err
}

// varNameRe is the placeholder grammar a transport template can name
// (llm/registry's placeholderRe: "{VAR}", uppercase only). A vars key in any
// other shape is one no substitution will ever reach, and the config writer's
// dry parse checks the $ENV syntax in the values, not the shape of the keys —
// so without this the entry lands in providers.toml and is silently ignored.
var varNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// validVarName refuses a key the placeholder grammar cannot name.
func validVarName(name string) error {
	if varNameRe.MatchString(name) {
		return nil
	}
	return fmt.Errorf("invalid variable name %q: a transport placeholder is {UPPERCASE_NAME}, so nothing would substitute it", name)
}

// validVarNames holds every key of a vars map to the grammar. Create writes
// each of them, so each has to be one a substitution could reach.
func validVarNames(vars map[string]string) error {
	for name := range vars {
		if err := validVarName(name); err != nil {
			return err
		}
	}
	return nil
}

// trimmedVars is the vars map as it is stored: every value trimmed, the way
// Edit trims each value it sets and the way base_url and api_key_env are
// trimmed beside it, so the two authoring paths write the same value.
func trimmedVars(vars map[string]string) map[string]string {
	if vars == nil {
		return nil
	}
	trimmed := make(map[string]string, len(vars))
	for name, value := range vars {
		trimmed[name] = strings.TrimSpace(value)
	}
	return trimmed
}

// validVarSets holds only the entries that SET a value. An edit spells a
// delete as an empty value (appwire.InstanceEditParams) and a delete writes
// nothing, so the key it names need not be one a substitution could reach —
// and a hand-authored key the grammar refuses is exactly the one the sheet
// has to be able to remove.
func validVarSets(vars map[string]string) error {
	for name, value := range vars {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := validVarName(name); err != nil {
			return err
		}
	}
	return nil
}

// credentialHeaderFrom reads the form's single NAME=VALUE credential header.
// The name must be an HTTP header token (registry.CheckCredentialHeaderName).
// The value must reference a $VARIABLE and carry no literal secret beside it:
// registry.CheckCredentialHeaderValue, shared with `evener providers add`, so
// neither authoring surface writes a key the other would refuse (spec §11.2)
// and this surface never saves a value the entry it broadcasts would have to
// omit. The refusal names the header, never its value.
func credentialHeaderFrom(field string) (map[string]string, error) {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil, nil
	}
	name, value, ok := strings.Cut(field, "=")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || name == "" {
		return nil, appwire.InvalidParams("credential header must be NAME=VALUE, as in Authorization=Bearer $PORTKEY_KEY")
	}
	if err := registry.CheckCredentialHeaderName(name); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	if err := registry.CheckCredentialHeaderValue(value); err != nil {
		return nil, appwire.InvalidParams(fmt.Sprintf("credential header %s: %v", name, err))
	}
	return map[string]string{name: value}, nil
}

// refuseWhenBroken stops every write while there is no registry to write
// against: a providers.toml that does not load (the hub has no way to rewrite
// a file it could not read without destroying what the user wrote — spec §10,
// §14.1), or a holder that has not loaded one yet. Every mutator asks this
// first, so none of them has to guard the reads that follow.
func (c *hubInstancesController) refuseWhenBroken() error {
	if c.reg.WritesRefused() {
		return fmt.Errorf("providers.toml cannot be edited until it loads: %w", c.reg.LoadError())
	}
	if c.reg.Get() == nil {
		return errors.New("providers.toml cannot be edited: the provider registry has not loaded")
	}
	return nil
}

// Create authors a new instance entry. APIKeyEnv is a variable name and
// CredentialHeader must reference a $VAR: a literal secret never crosses this
// boundary, and none is ever written to the file (spec §11.2).
//
// Every refusal that blames the fields the caller sent comes back as a wire
// error naming its class — InvalidParams for a field that is malformed or
// names something that does not exist, Conflict for a name already taken —
// matching how hubDirsCreate and the pin-section store classify the same
// shapes. A refusal about the hub's own state (the registry not loaded, a
// read or write failure) stays a plain error: that is not the caller's to
// fix.
func (c *hubInstancesController) Create(params appwire.InstanceCreateParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	if !registry.ValidInstanceName(name) {
		return appwire.InvalidParams(fmt.Sprintf("invalid instance name %q (lowercase, no slash)", params.Name))
	}
	base := strings.TrimSpace(params.Base)
	if _, ok := c.reg.Get().Provider(base); !ok {
		return appwire.InvalidParams(fmt.Sprintf("unknown base provider %q", params.Base))
	}
	credentialHeaders, err := credentialHeaderFrom(params.CredentialHeader)
	if err != nil {
		return err
	}
	apiKeyEnv := strings.TrimSpace(params.APIKeyEnv)
	if apiKeyEnv != "" {
		if err := registry.CheckAPIKeyEnvName(apiKeyEnv); err != nil {
			return appwire.InvalidParams(err.Error())
		}
	}
	if err := validVarNames(params.Vars); err != nil {
		return appwire.InvalidParams(err.Error())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	l, _, err := c.read()
	if err != nil {
		return err
	}
	if _, exists := l.Providers[name]; exists {
		return appwire.Conflict(fmt.Sprintf("instance %q already exists", name))
	}
	p := registry.Provider{
		ID:       name,
		Base:     base,
		Protocol: strings.TrimSpace(params.Protocol),
		Surface:  strings.TrimSpace(params.Surface),
		Transport: registry.Transport{
			BaseURL: strings.TrimSpace(params.BaseURL),
			Vars:    trimmedVars(params.Vars),
		},
	}
	if apiKeyEnv != "" {
		p.APIKeyEnv = []string{apiKeyEnv}
	}
	p.CredentialHeaders = credentialHeaders
	l.Providers[name] = p
	if err := c.writeLoadable(l); err != nil {
		return err
	}
	return c.reg.Reload()
}

// Edit applies the fields the form set, leaving every other authored key
// alone. Editing an instance that exists only from the environment authors a
// shadowing entry carrying those fields alone — never a base_url the form
// merely displayed, which would stop the instance inheriting its provider's
// key (spec §10, §11.3).
//
// A NewName re-keys the entry, follows the default pointer, and then moves
// the stored key and OAuth record (moveCredentials); it is refused for an
// implicit instance, an invalid name, a name any instance already has, and a
// name still holding a credential of its own (credentialsUnder).
//
// Refusals follow Create's convention (#717/#748): the ones that blame the
// fields the caller sent — an unknown name, an invalid vars key, an edit
// that would leave the instance unable to load — come back as
// appwire.InvalidParams; the hub's own faults (the registry not loaded, a
// read, write, or restore failure) stay plain errors.
func (c *hubInstancesController) Edit(params appwire.InstanceEditParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	if err := validVarSets(params.Vars); err != nil {
		return appwire.InvalidParams(err.Error())
	}
	// Not parsed at all under the clear flag: the clear branch below wins, as
	// it does for base URL, protocol, surface and api_key_env, so the value
	// riding along with it is one this request discards. Parsing it anyway
	// turns a removal into a refusal — and the value a user reaches for the
	// clear over is often the invalid one the parse would refuse.
	var credentialHeaders map[string]string
	if !params.ClearCredentialHeader {
		parsed, err := credentialHeaderFrom(params.CredentialHeader)
		if err != nil {
			return err
		}
		credentialHeaders = parsed
	}
	// api_key_env names an environment variable, never the key itself, and is
	// checked here for the same reason and under the same clear-flag rule as
	// the credential header above.
	apiKeyEnv := strings.TrimSpace(params.APIKeyEnv)
	if !params.ClearAPIKeyEnv && apiKeyEnv != "" {
		if err := registry.CheckAPIKeyEnvName(apiKeyEnv); err != nil {
			return appwire.InvalidParams(err.Error())
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// before is an independent parse from l below — a fresh read sharing no
	// maps with it — so if the edit parses fine but fails to load (#711),
	// writing it back restores exactly what was on disk before this call.
	before, _, err := c.read()
	if err != nil {
		return err
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}
	p, authored := l.Providers[name]
	if !authored {
		if _, ok := c.reg.Get().Instance(name); !ok {
			return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
		}
		p = registry.Provider{ID: name}
	}
	newName := strings.TrimSpace(params.NewName)
	renaming := newName != "" && newName != name
	if renaming {
		if !authored {
			return appwire.InvalidParams(fmt.Sprintf("instance %q comes from the environment and cannot be renamed", name))
		}
		if !registry.ValidInstanceName(newName) {
			return appwire.InvalidParams(fmt.Sprintf("invalid instance name %q (lowercase, no slash)", params.NewName))
		}
		if _, taken := l.Providers[newName]; taken {
			return appwire.Conflict(fmt.Sprintf("instance %q already exists", newName))
		}
		if _, taken := c.reg.Get().Instance(newName); taken {
			return appwire.Conflict(fmt.Sprintf("instance %q already exists", newName))
		}
		// The destination check below and the move at the end of this call
		// are one step: a credential written between them is one the check
		// never saw and the move would overwrite. Held for the rest of the
		// call, so the providers.toml write and the reload that follows it
		// sit inside the same held lock (hubAuthController.credMu).
		c.auth.credMu.Lock()
		defer c.auth.credMu.Unlock()
		// Both checks above ask which instances exist, and a credential can
		// outlive the instance it belonged to: providers.toml hand-edited
		// while credentials.toml or the OAuth state kept its entry. Under a
		// name the registry does not curate that leftover resolves no
		// instance, so it is invisible to them, and moveCredentials would
		// overwrite it. The refusal belongs here rather than there: by the
		// time moveCredentials runs the file is re-keyed and the registry
		// reloaded, so there is no longer anything to refuse.
		if held := c.credentialsUnder(newName); len(held) > 0 {
			return appwire.Conflict(fmt.Sprintf("renaming %q to %q would overwrite %s; clear that first", name, newName, strings.Join(held, " and ")))
		}
	}
	if params.ClearBaseURL {
		// Drops the authored override and goes back to the registry
		// default, restoring spec §10's credential inheritance from the
		// base provider (#711). Additive over BaseURL's existing "empty
		// means unchanged" (v3): the two are never both meaningful in the
		// same request (appwire.InstanceEditParams doc comment).
		p.Transport.BaseURL = ""
	} else if v := strings.TrimSpace(params.BaseURL); v != "" {
		p.Transport.BaseURL = v
	}
	if params.ClearProtocol {
		p.Protocol = ""
	} else if v := strings.TrimSpace(params.Protocol); v != "" {
		p.Protocol = v
	}
	if params.ClearSurface {
		p.Surface = ""
	} else if v := strings.TrimSpace(params.Surface); v != "" {
		p.Surface = v
	}
	if params.ClearAPIKeyEnv {
		p.APIKeyEnv = nil
	} else if apiKeyEnv != "" {
		p.APIKeyEnv = []string{apiKeyEnv}
	}
	if params.ClearCredentialHeader {
		p.CredentialHeaders = nil
	} else if credentialHeaders != nil {
		p.CredentialHeaders = credentialHeaders
	}
	// An empty value deletes the variable (appwire.InstanceEditParams);
	// anything else is set over whatever was authored before, trimmed as
	// base_url and api_key_env are — the delete is decided on the trimmed
	// value, so storing the untrimmed one would let a value that only just
	// escaped the delete land as one padded with spaces.
	for key, value := range params.Vars {
		value = strings.TrimSpace(value)
		if value == "" {
			delete(p.Transport.Vars, key)
			continue
		}
		if p.Transport.Vars == nil {
			p.Transport.Vars = map[string]string{}
		}
		p.Transport.Vars[key] = value
	}
	if renaming {
		// An entry with no base inherits protocol, surface and models from the
		// curated provider its own name matches (spec §4.2). The new name
		// matches nothing, so the inheritance would vanish with the old name
		// and the reload below would refuse the rename. Writing down the base
		// the entry was already resolving against keeps the effective
		// configuration identical; it is not a re-base onto a different
		// provider, which spec §7 puts out of scope.
		if p.Base == "" {
			if _, curated := c.reg.Get().Provider(name); curated {
				p.Base = name
			}
		}
		// The same rule pointed at the new name: an entry the rule above left
		// without a base has none to pin its configuration, so under a curated
		// id it would start inheriting that provider's protocol, transport,
		// models and credential resolution instead of resolving its own
		// fields. Neither taken-name check refuses it — a curated provider
		// with no credential is not an instance — and nothing has moved yet,
		// so the refusal costs nothing to make here.
		if p.Base == "" {
			if _, curated := c.reg.Get().Provider(newName); curated {
				return appwire.InvalidParams(fmt.Sprintf("%q is a curated provider id; an instance named after it would inherit its configuration. Give the instance an explicit base or choose another name.", newName))
			}
		}
		// The map key is the instance name providers.toml is written under;
		// the default pointer follows so the file still loads.
		delete(l.Providers, name)
		p.ID = newName
		if l.Default == name {
			l.Default = newName
		}
		l.Providers[newName] = p
	} else {
		l.Providers[name] = p
	}
	if err := c.writeLoadable(l); err != nil {
		return err
	}
	if err := c.reg.Reload(); err != nil {
		// writeLoadable's dry parse only checks TOML syntax against the
		// registry schema; it does not resolve the config the way Reload
		// does. A standalone instance (no base, and its own name is not a
		// registry id either) that just lost its only base_url is a config
		// that parses fine but cannot resolve an endpoint (llm/registry:
		// "no base URL: set base_url = … or base = <registry id>"), and one
		// bad instance record fails the whole reload, not just this one
		// (#711). Restore the file this call just overwrote instead of
		// leaving every instance operation refused by a config only this
		// edit produced.
		if restoreErr := c.write(before); restoreErr != nil {
			return fmt.Errorf("%w (and restoring the previous config failed: %w)", err, restoreErr)
		}
		_ = c.reg.Reload() // best-effort: put the last-good registry view back
		return appwire.InvalidParams(fmt.Sprintf("this edit would leave %q unable to load: %v", name, err))
	}
	if renaming {
		moveErr := c.moveCredentials(name, newName)
		// The reload above ran while the stored key and OAuth record still
		// sat under the old name, so a curated provider this instance had
		// shadowed could resolve a credential and reappear as a phantom
		// implicit instance — one that also makes renaming back a sticky
		// Conflict. Remove clears credentials before its reload; a rename
		// cannot, because a failed reload restores the file and the
		// credentials would already have moved.
		if err := c.reg.Reload(); err != nil && moveErr == nil {
			// Everything this rename writes is already written, so it is as
			// persisted as one that ended cleanly and is announced the same
			// way.
			return renamePersistedError{err}
		}
		return moveErr
	}
	return nil
}

// credentialsUnder names the credentials already filed under name, in the
// vocabulary describeImplicit uses for the same two sources. It is what a
// rename onto name would overwrite, so the caller can go clear the one it
// names. A record that exists but does not read back counts as present:
// not-found is the only signal that nothing is there, and overwriting a
// credential the hub merely failed to read is the same loss.
func (c *hubInstancesController) credentialsUnder(name string) []string {
	var held []string
	if _, ok := c.auth.creds.Get(name); ok {
		held = append(held, fmt.Sprintf("a credentials.toml entry for %q", name))
	}
	if _, err := c.auth.loadAuth(c.auth.stateDir, name); !errors.Is(err, authopenai.ErrAuthNotFound) {
		held = append(held, fmt.Sprintf("an OAuth record for %q", name))
	}
	return held
}

// renamePersistedError is a rename that reached the file: providers.toml
// carries the new name, and what is unfinished is either the credential move
// or the reload that would refresh the hub's own view of what the move
// changed. Every other client's instance list is stale by exactly as much as
// it would be after a clean rename, so the RPC handler broadcasts on it and
// still returns it, leaving the client that asked with the leftover to deal
// with.
type renamePersistedError struct{ err error }

func (e renamePersistedError) Error() string { return e.err.Error() }

func (e renamePersistedError) Unwrap() error { return e.err }

// moveCredentials carries an instance's stored key and OAuth record to its
// new name after a rename. It runs once providers.toml is written and
// reloaded, with credMu held by the caller: the config is already renamed, so
// a failure here is reported as what was left behind rather than undone — the
// list stays consistent with the file, and a leftover stays reachable under
// the old name through evener/auth/apiKey/clear or the state directory. That
// report is a renamePersistedError, which is what tells the RPC handler the
// rename is on disk however this call ends.
// Nothing it calls takes credMu, which the caller still holds.
func (c *hubInstancesController) moveCredentials(oldName, newName string) error {
	var problems []string
	// One persist, so the key is never briefly filed under both names or
	// neither: a copy-then-clear pair whose second half failed would leave
	// the old name resolving a credential the config no longer names.
	if err := c.auth.creds.Move(oldName, newName); err != nil {
		problems = append(problems, fmt.Sprintf("stored key not copied: %v", err))
	}
	record, err := c.auth.loadAuth(c.auth.stateDir, oldName)
	switch {
	case errors.Is(err, authopenai.ErrAuthNotFound):
	case err != nil:
		problems = append(problems, fmt.Sprintf("OAuth record not read: %v", err))
	default:
		// The record's provider field names the instance it belongs to (the
		// OAuth completion paths set it), so it follows the rename.
		record.Provider = newName
		if err := c.auth.saveAuth(c.auth.stateDir, newName, record); err != nil {
			problems = append(problems, fmt.Sprintf("OAuth record not copied: %v", err))
		} else if _, err := c.auth.deleteAuth(c.auth.stateDir, oldName); err != nil {
			problems = append(problems, fmt.Sprintf("OAuth record for %q left behind: %v", oldName, err))
		}
	}
	if len(problems) > 0 {
		return renamePersistedError{fmt.Errorf("renamed %q to %q, but: %s", oldName, newName, strings.Join(problems, "; "))}
	}
	return nil
}

// Remove deletes an authored instance, its stored key and its OAuth record.
// An instance that exists from the environment has no entry to delete, so the
// refusal says what to unset instead (spec §5.1). A name that resolves to no
// instance follows Create and Edit's convention (#717/#748): the caller sent
// it, so it comes back as appwire.InvalidParams.
func (c *hubInstancesController) Remove(params appwire.InstanceRemoveParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	// The name is forwarded to authopenai.DeleteAuth, which joins it into
	// stateDir/auth/<name>.json; validating it here is what keeps a name
	// containing path separators from deleting an arbitrary file.
	name := strings.TrimSpace(params.Name)
	if !registry.ValidInstanceName(name) {
		return appwire.InvalidParams(fmt.Sprintf("invalid instance name %q (lowercase, no slash)", params.Name))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// The lookup names what this call deletes - the authored entry, the
	// stored key and the OAuth record under this name - so it is made under
	// the lock that holds the deletion, as Edit's are: a rename landing
	// between the two would hand the deletion to whatever holds the name
	// afterwards.
	inst, ok := c.reg.Get().Instance(name)
	if !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	if inst.Implicit {
		return fmt.Errorf("%s exists from the environment (%s); unset it or remove the OAuth record instead of deleting the instance", name, describeImplicit(inst))
	}

	// Read the authored layer before anything is deleted: this is a pure read,
	// so a failure here leaves nothing to undo, and it happens inside c.mu, so
	// the layer it returns is still the one this removal edits.
	l, _, err := c.read()
	if err != nil {
		return err
	}

	// Held exclusively across the credential cleanup, the providers.toml write
	// and the reload that follows it, the way a rename holds it across its
	// check and re-key (see hubAuthController.credMu). A credential writer
	// already in flight finishes first, and the cleanup below removes whatever
	// it wrote; one that starts afterwards reads the reloaded registry, where
	// this instance no longer exists. Holding only the read side left a writer
	// that had already passed its checks free to store a key after the
	// cleanup, leaving a credential behind under a name the removal had just
	// deleted.
	c.auth.credMu.Lock()
	defer c.auth.credMu.Unlock()

	// Credentials first, then the authored entry: a cleanup that cannot
	// complete fails the removal while the instance and its name still exist,
	// so the caller can retry it. The reverse order would report a deletion
	// that only half happened and leave the credential under a name nothing
	// curates - invisible until a later instance of that name inherits it.
	// What the cleanup is about to delete is captured first, because either
	// half of it can still fail - the cleanup itself, or the write below -
	// and both leave [providers.<name>] in place and tell the caller the
	// removal failed, so the instance the caller still has must still
	// authenticate. Capture and restore both sit inside this held lock, so no
	// writer can slip between them.
	storedKey, hasStoredKey := c.auth.creds.Get(name)
	oauthBytes, hasOAuth, err := c.captureOAuthFile(name)
	if err != nil {
		return err
	}

	removed, err := c.removeCredentials(name)
	if err != nil {
		return c.restoreFailedRemoval(name, storedKey, hasStoredKey && removed.storedKey, oauthBytes, hasOAuth && removed.oauthRecord, err)
	}

	delete(l.Providers, name)
	// A `default` naming the instance just removed would fail the next load,
	// so it goes with it; the ranking of §5.1 picks the replacement.
	if l.Default == name {
		l.Default = ""
	}
	if err := c.writeLoadable(l); err != nil {
		return c.restoreFailedRemoval(name, storedKey, hasStoredKey, oauthBytes, hasOAuth, err)
	}
	return c.reg.Reload()
}

// captureOAuthFile reads the OAuth state file a removal's cleanup is about to
// unlink, so a later failure can write those bytes back. It captures the raw
// bytes rather than the parsed record because DeleteAuth deletes by path: a
// record the hub cannot parse (corrupt) or validate is one it will still
// delete, and only the bytes can put it back. A missing file is (nil, false,
// nil); one that exists but cannot be read is refused here, before anything is
// deleted, because the removal cannot promise to restore what it cannot read.
func (c *hubInstancesController) captureOAuthFile(name string) ([]byte, bool, error) {
	raw, err := os.ReadFile(authopenai.AuthFilePath(c.auth.stateDir, name))
	if err == nil {
		return raw, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("remove %s: read OAuth state to preserve it: %w", name, err)
}

// restoreFailedRemoval puts back what the cleanup deleted after a failure that
// left the instance authored, and folds whatever it could not restore into the
// error the caller sees: the removal did not happen, so the instance must
// still authenticate, and a caller told only that the removal failed would
// have no way to know that it did not. Its callers pass only the layers the
// failure actually deleted, so this never rewrites - and never reports a
// failure to rewrite - a credential that is still where it was.
func (c *hubInstancesController) restoreFailedRemoval(name, storedKey string, hasStoredKey bool, oauthBytes []byte, hasOAuth bool, cause error) error {
	var problems []string
	if hasStoredKey {
		if err := c.auth.setCredential(name, storedKey); err != nil {
			problems = append(problems, fmt.Sprintf("its stored key could not be restored (%v)", err))
		}
	}
	if hasOAuth {
		if err := writeAuthFile(authopenai.AuthFilePath(c.auth.stateDir, name), oauthBytes); err != nil {
			problems = append(problems, fmt.Sprintf("its OAuth record could not be restored (%v)", err))
		}
	}
	if len(problems) == 0 {
		return cause
	}
	return fmt.Errorf("%w; the instance is still configured, but %s", cause, strings.Join(problems, " and "))
}

// writeAuthFile puts an OAuth state file back exactly as it was: 0600 like
// SaveAuth writes, and synced, because what it restores is a credential file
// whose loss is the reason it exists.
func writeAuthFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// removeCredentials deletes the credential layers filed under a name whose
// instance is being removed: the stored key and the OAuth record. Both go
// through the controller's seams, like every other path that removes a
// credential (Logout, ApiKeyClear), and neither failure is tolerated - a
// credential left behind sits under a name nothing curates, and a later
// instance holding that name would inherit it. A missing entry is not a
// failure: Store.Clear deletes and persists, and DeleteAuth reports not-found
// as (false, nil).
//
// It reports which layers it actually deleted even when it fails, because its
// caller restores exactly those: Store.Clear puts its own entry back when the
// persist fails (nothing deleted), while a failed DeleteAuth leaves its file
// in place - rewriting either would be a false alarm on a disk that is already
// refusing writes.
func (c *hubInstancesController) removeCredentials(name string) (deletedCredentials, error) {
	var deleted deletedCredentials
	if err := c.auth.clearCredential(name); err != nil {
		return deleted, fmt.Errorf("remove %s: clear stored credential: %w", name, err)
	}
	deleted.storedKey = true
	if _, err := c.auth.deleteAuth(c.auth.stateDir, name); err != nil {
		return deleted, fmt.Errorf("remove %s: delete OAuth state: %w", name, err)
	}
	deleted.oauthRecord = true
	return deleted, nil
}

// deletedCredentials names which credential layers a removal's cleanup
// actually removed, so a restore rewrites only those.
type deletedCredentials struct {
	storedKey   bool
	oauthRecord bool
}

// describeImplicit names what makes an implicit instance exist, so the remove
// refusal can say what to take away.
func describeImplicit(inst registry.Instance) string {
	switch src := inst.CredentialSource; {
	case strings.HasPrefix(src, "env:"):
		return src
	case src == "oauth":
		return "OAuth record for " + inst.Name
	case src == "store":
		return "credentials.toml entry for " + inst.Name
	default:
		return "credential source " + src
	}
}

// SetDefault records which instance a bare model reference resolves on. A
// name that resolves to no instance follows Create and Edit's convention
// (#717/#748): the caller sent it, so it comes back as appwire.InvalidParams.
func (c *hubInstancesController) SetDefault(params appwire.InstanceSetDefaultParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Checked under the lock that holds the write: a rename landing between
	// the two would leave a default naming an instance that has moved, which
	// the next load refuses while it sits on disk.
	if _, ok := c.reg.Get().Instance(name); !ok {
		return appwire.InvalidParams(fmt.Sprintf("instance %q not found", name))
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}
	l.Default = name
	if err := c.writeLoadable(l); err != nil {
		return err
	}
	return c.reg.Reload()
}
