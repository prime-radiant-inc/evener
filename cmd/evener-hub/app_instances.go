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
		for _, inst := range r.Instances() {
			entries = append(entries, c.entryFor(inst))
		}
		for _, id := range r.ProviderIDs() {
			p, ok := r.Provider(id)
			if !ok {
				continue
			}
			providers = append(providers, appwire.ProviderDescriptor{
				ID:        id,
				Name:      p.Name,
				Protocol:  p.Protocol,
				Auth:      p.Transport.Auth,
				VarsEnv:   slices.Sorted(maps.Values(p.Transport.VarsEnv)),
				APIKeyEnv: append([]string(nil), p.APIKeyEnv...),
				Implicit:  registry.BoolValue(p.Implicit),
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

// entryFor is the wire view of one instance: the registry's own description
// plus the credential status the auth controller derives for it.
func (c *hubInstancesController) entryFor(inst registry.Instance) appwire.InstanceEntry {
	status := c.auth.instanceStatus(inst)
	return appwire.InstanceEntry{
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
		StoredEmail:        status.StoredEmail,
		CredentialRequired: inst.Auth != registry.AuthNone && inst.Auth != registry.AuthOptionalBearer,
		Warnings:           inst.Warnings,
	}
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

// validVarNames refuses a vars map carrying a key the placeholder grammar
// cannot name.
func validVarNames(vars map[string]string) error {
	for name := range vars {
		if !varNameRe.MatchString(name) {
			return fmt.Errorf("invalid variable name %q: a transport placeholder is {UPPERCASE_NAME}, so nothing would substitute it", name)
		}
	}
	return nil
}

// credentialHeaderFrom reads the form's single NAME=VALUE credential header.
// The value must reference a $VARIABLE and carry no literal secret beside it:
// one rule, registry.CheckCredentialHeaderValue, shared with
// `evener providers add`, so neither authoring surface writes a key the other
// would refuse (spec §11.2). The refusal names the header, never its value.
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
			Vars:    params.Vars,
		},
	}
	if v := strings.TrimSpace(params.APIKeyEnv); v != "" {
		p.APIKeyEnv = []string{v}
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
func (c *hubInstancesController) Edit(params appwire.InstanceEditParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	if err := validVarNames(params.Vars); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	l, _, err := c.read()
	if err != nil {
		return err
	}
	p, authored := l.Providers[name]
	if !authored {
		if _, ok := c.reg.Get().Instance(name); !ok {
			return fmt.Errorf("instance %q not found", name)
		}
		p = registry.Provider{ID: name}
	}
	if v := strings.TrimSpace(params.BaseURL); v != "" {
		p.Transport.BaseURL = v
	}
	if v := strings.TrimSpace(params.Protocol); v != "" {
		p.Protocol = v
	}
	if v := strings.TrimSpace(params.Surface); v != "" {
		p.Surface = v
	}
	if len(params.Vars) > 0 {
		if p.Transport.Vars == nil {
			p.Transport.Vars = map[string]string{}
		}
		maps.Copy(p.Transport.Vars, params.Vars)
	}
	l.Providers[name] = p
	if err := c.writeLoadable(l); err != nil {
		return err
	}
	return c.reg.Reload()
}

// Remove deletes an authored instance, its stored key and its OAuth record.
// An instance that exists from the environment has no entry to delete, so the
// refusal says what to unset instead (spec §5.1).
func (c *hubInstancesController) Remove(params appwire.InstanceRemoveParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	// The name is forwarded to authopenai.DeleteAuth, which joins it into
	// stateDir/auth/<name>.json; validating it here is what keeps a name
	// containing path separators from deleting an arbitrary file.
	name := strings.TrimSpace(params.Name)
	if !registry.ValidInstanceName(name) {
		return fmt.Errorf("invalid instance name %q (lowercase, no slash)", params.Name)
	}
	inst, ok := c.reg.Get().Instance(name)
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}
	if inst.Implicit {
		return fmt.Errorf("%s exists from the environment (%s); unset it or remove the OAuth record instead of deleting the instance", name, describeImplicit(inst))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	l, _, err := c.read()
	if err != nil {
		return err
	}
	delete(l.Providers, name)
	// A `default` naming the instance just removed would fail the next load,
	// so it goes with it; the ranking of §5.1 picks the replacement.
	if l.Default == name {
		l.Default = ""
	}
	if err := c.writeLoadable(l); err != nil {
		return err
	}

	// Clear stored credentials (ignore errors for missing entries).
	_ = c.auth.creds.Clear(name)
	// Delete OAuth state file as best-effort: DeleteAuth already ignores
	// not-found. Any other error is logged but does not fail Remove, since
	// the instance is already gone from providers.toml.
	if _, err := authopenai.DeleteAuth(c.auth.stateDir, name); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] remove %s: delete OAuth state: %v\n", name, err)
	}
	return c.reg.Reload()
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

// SetDefault records which instance a bare model reference resolves on.
func (c *hubInstancesController) SetDefault(params appwire.InstanceSetDefaultParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	if _, ok := c.reg.Get().Instance(name); !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
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
