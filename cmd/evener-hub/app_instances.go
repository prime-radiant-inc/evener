package hub

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"sort"
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
	readConfig          func(string) (*registry.Layer, bool, error)
	writeConfig         func(string, *registry.Layer) error
	mu                  sync.Mutex
}

func (c *hubInstancesController) read() (*registry.Layer, bool, error) {
	if c.readConfig != nil {
		return c.readConfig(c.providersConfigPath)
	}
	return registry.ReadConfigFile(c.providersConfigPath)
}

func (c *hubInstancesController) write(l *registry.Layer) error {
	if c.writeConfig != nil {
		return c.writeConfig(c.providersConfigPath, l)
	}
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
				VarsEnv:   sortedValues(p.Transport.VarsEnv),
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
		WritesRefused:      c.reg.WritesRefused(),
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

// sortedValues returns a map's values in sorted order.
func sortedValues(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
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

// validatedProtocolAndSurface trims the two vocabulary fields a form can set
// and refuses anything the registry would reject at load. Without this a typo
// is written, the reload that follows fails, and refuseWhenBroken then refuses
// the corrective edit too — the pane locked out of its own recovery.
func validatedProtocolAndSurface(protocol, surface string) (string, string, error) {
	protocol, surface = strings.TrimSpace(protocol), strings.TrimSpace(surface)
	if protocol != "" && !registry.ValidProtocol(protocol) {
		return "", "", fmt.Errorf("invalid protocol %q (one of %s)", protocol,
			strings.Join([]string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses, registry.ProtocolAnthropic, registry.ProtocolGoogle}, ", "))
	}
	if surface != "" && !registry.ValidSurface(surface) {
		return "", "", fmt.Errorf("invalid surface %q (one of %s)", surface,
			strings.Join([]string{registry.SurfaceOpenAI, registry.SurfaceAnthropic, registry.SurfaceGoogle, registry.SurfaceGeneric}, ", "))
	}
	return protocol, surface, nil
}

// refuseWhenBroken stops every write while providers.toml does not load: the
// hub has no way to rewrite a file it could not read without destroying what
// the user wrote (spec §10, §14.1).
func (c *hubInstancesController) refuseWhenBroken() error {
	if c.reg.WritesRefused() {
		return fmt.Errorf("providers.toml cannot be edited until it loads: %w", c.reg.LoadError())
	}
	return nil
}

// Create authors a new instance entry. APIKeyEnv is a variable name and
// CredentialHeader must reference a $VAR: a literal secret never crosses this
// boundary, and none is ever written to the file (spec §11.2).
func (c *hubInstancesController) Create(params appwire.InstanceCreateParams) error {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	if !registry.ValidInstanceName(name) {
		return fmt.Errorf("invalid instance name %q (lowercase, no slash)", params.Name)
	}
	base := strings.TrimSpace(params.Base)
	if _, ok := c.reg.Get().Provider(base); !ok {
		return fmt.Errorf("unknown base provider %q", params.Base)
	}
	if params.CredentialHeader != "" && !strings.Contains(params.CredentialHeader, "$") {
		return errors.New("credential header must reference a $VARIABLE, never a literal secret")
	}
	protocol, surface, err := validatedProtocolAndSurface(params.Protocol, params.Surface)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	l, _, err := c.read()
	if err != nil {
		return err
	}
	if _, exists := l.Providers[name]; exists {
		return fmt.Errorf("instance %q already exists", name)
	}
	p := registry.Provider{
		ID:       name,
		Base:     base,
		Protocol: protocol,
		Surface:  surface,
		Transport: registry.Transport{
			BaseURL: strings.TrimSpace(params.BaseURL),
			Vars:    params.Vars,
		},
	}
	if v := strings.TrimSpace(params.APIKeyEnv); v != "" {
		p.APIKeyEnv = []string{v}
	}
	if k, v, ok := strings.Cut(params.CredentialHeader, "="); ok && strings.TrimSpace(k) != "" {
		p.CredentialHeaders = map[string]string{strings.TrimSpace(k): strings.TrimSpace(v)}
	}
	l.Providers[name] = p
	if err := c.write(l); err != nil {
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
	protocol, surface, err := validatedProtocolAndSurface(params.Protocol, params.Surface)
	if err != nil {
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
	if protocol != "" {
		p.Protocol = protocol
	}
	if surface != "" {
		p.Surface = surface
	}
	if len(params.Vars) > 0 {
		if p.Transport.Vars == nil {
			p.Transport.Vars = map[string]string{}
		}
		maps.Copy(p.Transport.Vars, params.Vars)
	}
	l.Providers[name] = p
	if err := c.write(l); err != nil {
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
	if err := c.write(l); err != nil {
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
	if err := c.write(l); err != nil {
		return err
	}
	return c.reg.Reload()
}
