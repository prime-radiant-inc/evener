package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CredentialSource is the credentials-store view the registry needs: the
// file-layer entry stored under an instance name (spec §10). Environment
// lookups are the registry's own job.
type CredentialSource interface {
	Lookup(instance string) (value string, ok bool)
}

// Fetcher fetches models.dev for the runtime cache (spec §6.4). notModified
// is true when the server answered 304 for etag.
type Fetcher func(ctx context.Context, etag string) (body []byte, newEtag string, notModified bool, err error)

// Option configures Load.
type Option func(*options)

type options struct {
	configPath  string
	configSet   bool
	noUserLayer bool
	stateRoot   string
	env         func(string) (string, bool)
	creds       CredentialSource
	fetcher     Fetcher
	offline     *bool
	instances   map[string]Provider
	now         func() time.Time
	snapshot    []byte
	overlay     []byte
}

// WithConfigPath reads providers.toml from path instead of
// EVENER_PROVIDERS_CONFIG / the default config root.
func WithConfigPath(path string) Option {
	return func(o *options) { o.configPath, o.configSet = path, true }
}

// WithNoUserLayer loads no providers.toml at all (spec §10's "present and
// empty" state, forced).
func WithNoUserLayer() Option { return func(o *options) { o.noUserLayer = true } }

// WithStateRoot sets the state root that holds catalog/ (spec §6.4).
func WithStateRoot(dir string) Option { return func(o *options) { o.stateRoot = dir } }

// WithEnv replaces os.LookupEnv for every environment read.
func WithEnv(lookup func(string) (string, bool)) Option { return func(o *options) { o.env = lookup } }

// WithCredentials supplies the credentials store's file layer.
func WithCredentials(c CredentialSource) Option { return func(o *options) { o.creds = c } }

// WithFetcher injects the models.dev fetcher and, on its own, opts a test
// back into the refresh path (spec §6.4).
func WithFetcher(f Fetcher) Option { return func(o *options) { o.fetcher = f } }

// WithOffline sets whether Load may start the background refresh.
func WithOffline(offline bool) Option { return func(o *options) { o.offline = &offline } }

// WithInstances injects named instances before layering (spec §5.2); they
// behave like [providers.X] entries and shadow file entries of the same name.
func WithInstances(instances map[string]Provider) Option {
	return func(o *options) { o.instances = instances }
}

// WithNow replaces time.Now for cache-age decisions.
func WithNow(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithSnapshot replaces the embedded models.dev JSON (tests).
func WithSnapshot(raw []byte) Option { return func(o *options) { o.snapshot = raw } }

// WithOverlay replaces the embedded curated overlay (tests).
func WithOverlay(data []byte) Option { return func(o *options) { o.overlay = data } }

// Registry is the loaded, layered provider registry (spec §5). It is
// immutable after Load except for the live listings Resolve consults.
type Registry struct {
	presets      map[string]Transport
	defaultOrder []string
	topGlobs     map[string]map[string]Model // layer tag → glob rows
	curated      map[string]*record          // registry ids (layers 1–3)
	explicit     map[string]*record          // user-layer and injected instances
	userDefault  string
	userNote     string
	env          func(string) (string, bool)
	creds        CredentialSource
	stateRoot    string
	catalogTag   string
	catalogMeta  Meta
	warnings     []string
	instances    map[string]*instance   //nolint:unused // filled by Task 10
	live         map[string]liveListing //nolint:unused // filled by Task 8
}

// record is one merged provider: its head (scalar fields folded across the
// base chain and the layers) plus the per-layer capability contributions
// that Resolve replays in order (spec §4.1, §4.2).
type record struct {
	name       string
	providerID string
	curated    bool
	injected   bool
	head       Provider
	userVars   map[string]string
	layers     []capLayer
	notes      []string
}

// capLayer is one layer's contribution to a record.
type capLayer struct {
	tag         string
	owner       string
	provider    Caps
	rows        map[string]Model
	resetFields bool
}

func defaultOptions() *options {
	return &options{env: os.LookupEnv, now: time.Now}
}

// defaultConfigRoot mirrors cmdutil.DefaultConfigRoot, which the llm module
// cannot import: $XDG_CONFIG_HOME/evener, else ~/.config/evener.
func defaultConfigRoot(env func(string) (string, bool)) string {
	if base, ok := env("XDG_CONFIG_HOME"); ok && base != "" {
		return filepath.Join(base, "evener")
	}
	home, _ := env("HOME")
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "evener")
}

// defaultStateRoot mirrors cmdutil.DefaultStateRoot: $XDG_STATE_HOME/evener,
// else ~/.local/state/evener.
func defaultStateRoot(env func(string) (string, bool)) string {
	if base, ok := env("XDG_STATE_HOME"); ok && base != "" {
		return filepath.Join(base, "evener")
	}
	home, _ := env("HOME")
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".local", "state", "evener")
}

// Load assembles the registry from the embedded snapshot, the curated
// overlay, and the user layer (spec §5). The runtime cache and refresh are
// wired in by Task 11.
func Load(opts ...Option) (*Registry, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	if o.stateRoot == "" {
		o.stateRoot = defaultStateRoot(o.env)
	}
	r := &Registry{
		presets: map[string]Transport{}, topGlobs: map[string]map[string]Model{},
		curated: map[string]*record{}, explicit: map[string]*record{},
		env: o.env, creds: o.creds, stateRoot: o.stateRoot,
	}
	raw, meta := o.snapshot, Meta{}
	if raw == nil {
		var err error
		raw, meta, err = EmbeddedSnapshot()
		if err != nil {
			return nil, err
		}
	}
	upstream, err := FromModelsDev(raw)
	if err != nil {
		return nil, err
	}
	r.catalogTag, r.catalogMeta = LayerSnapshot, meta
	upstreamByID := make(map[string]Provider, len(upstream))
	for _, p := range upstream {
		upstreamByID[p.ID] = p
	}
	overlayData := o.overlay
	if overlayData == nil {
		overlayData = EmbeddedOverlay()
	}
	ov, err := ParseOverlay(overlayData)
	if err != nil {
		return nil, err
	}
	r.presets, r.defaultOrder, r.topGlobs[LayerOverlay] = ov.Transports, ov.DefaultOrder, ov.TopGlobs

	user, note, err := loadUserLayer(o)
	if err != nil {
		return nil, err
	}
	r.userNote = note
	if len(o.instances) > 0 {
		if user == nil {
			user = &Layer{Tag: LayerConfig, Providers: map[string]Provider{}}
		}
		for name, p := range o.instances {
			p.ID = name
			user.Providers[name] = p
		}
	}
	if user != nil {
		r.userDefault = user.Default
		r.topGlobs[LayerConfig] = user.TopGlobs
	}

	ids := make([]string, 0, len(upstreamByID)+len(ov.Providers))
	for id := range upstreamByID {
		ids = append(ids, id)
	}
	for id := range ov.Providers {
		if _, ok := upstreamByID[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := r.curatedRecord(id, upstreamByID, ov, map[string]bool{}); err != nil {
			return nil, err
		}
	}
	if user != nil {
		names := make([]string, 0, len(user.Providers))
		for name := range user.Providers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			rec, err := r.instanceRecord(user.Providers[name])
			if err != nil {
				return nil, err
			}
			if o.instances != nil {
				_, rec.injected = o.instances[name]
			}
			r.explicit[name] = rec
		}
	}
	for _, rec := range r.allRecords() {
		if err := r.validateRecord(rec); err != nil {
			return nil, err
		}
	}
	for _, rec := range r.allRecords() {
		r.computeHidden(rec)
	}
	return r, nil
}

func loadUserLayer(o *options) (*Layer, string, error) {
	path := ""
	switch {
	case o.noUserLayer:
		return nil, "user layer: none (disabled)", nil
	case o.configSet:
		path = o.configPath
	default:
		v, ok := o.env("EVENER_PROVIDERS_CONFIG")
		switch {
		case ok && v == "":
			return nil, "user layer: none (EVENER_PROVIDERS_CONFIG is empty)", nil
		case ok:
			path = v
		default:
			path = filepath.Join(defaultConfigRoot(o.env), "providers.toml")
		}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Sprintf("user layer: none (%s does not exist)", path), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	l, err := ParseConfig(data)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	return l, "user layer: " + path, nil
}

func (r *Registry) allRecords() []*record {
	out := make([]*record, 0, len(r.curated)+len(r.explicit))
	for _, id := range sortedKeys(r.curated) {
		out = append(out, r.curated[id])
	}
	for _, name := range sortedKeys(r.explicit) {
		out = append(out, r.explicit[name])
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// curatedRecord builds (memoized) the merged record of a registry id:
// its base chain first, then its upstream entry, then its overlay entry.
func (r *Registry) curatedRecord(id string, upstream map[string]Provider, ov *Layer, visiting map[string]bool) (*record, error) {
	if rec, ok := r.curated[id]; ok {
		return rec, nil
	}
	if visiting[id] {
		return nil, fmt.Errorf("providers.%s: base cycle", id)
	}
	visiting[id] = true
	up, hasUp := upstream[id]
	ovp, hasOv := ov.Providers[id]
	if !hasUp && !hasOv {
		return nil, fmt.Errorf("unknown provider %q", id)
	}
	rec := &record{name: id, providerID: id, curated: true, head: Provider{ID: id, Models: map[string]Model{}}}
	if hasOv && ovp.Base != "" {
		base, err := r.curatedRecord(ovp.Base, upstream, ov, visiting)
		if err != nil {
			return nil, fmt.Errorf("providers.%s: base: %w", id, err)
		}
		rec.inherit(base)
	}
	if hasUp {
		if err := rec.fold(up, r.catalogTag, r.presets); err != nil {
			return nil, err
		}
	}
	if hasOv {
		if err := rec.fold(ovp, LayerOverlay, r.presets); err != nil {
			return nil, err
		}
	}
	r.curated[id] = rec
	return rec, nil
}

// instanceRecord builds a user-layer instance: an explicit base wins over a
// name match; base names resolve against the curated registry only (spec §4.2).
func (r *Registry) instanceRecord(p Provider) (*record, error) {
	rec := &record{name: p.ID, head: Provider{ID: p.ID, Models: map[string]Model{}}}
	baseID := p.Base
	if baseID == "" {
		if _, ok := r.curated[p.ID]; ok {
			baseID = p.ID
		}
	}
	if baseID != "" {
		base, ok := r.curated[baseID]
		if !ok {
			return nil, fmt.Errorf("providers.%s: base %q is not a registry id", p.ID, baseID)
		}
		rec.inherit(base)
		rec.providerID = baseID
	}
	if err := rec.fold(p, LayerConfig, r.presets); err != nil {
		return nil, err
	}
	if rec.head.Protocol == "" {
		return nil, fmt.Errorf("providers.%s: no protocol: set protocol = … or base = <registry id>", p.ID)
	}
	if rec.head.Transport.BaseURL == "" {
		return nil, fmt.Errorf("providers.%s: no base URL: set base_url = … or base = <registry id>", p.ID)
	}
	return rec, nil
}

// inherit copies the base record's merged form (spec §4.2). Implicit and
// Hidden are never inherited; ID stays the record's own.
func (rec *record) inherit(base *record) {
	h := base.head
	h.ID = rec.name
	h.Implicit = nil
	h.Hidden = false
	h.Models = make(map[string]Model, len(base.head.Models))
	for id, m := range base.head.Models {
		h.Models[id] = cloneModel(m)
	}
	h.Transport = cloneTransport(base.head.Transport)
	h.Headers = mergeStringMap(nil, base.head.Headers)
	h.CredentialHeaders = mergeStringMap(nil, base.head.CredentialHeaders)
	h.APIKeyEnv = append([]string(nil), base.head.APIKeyEnv...)
	rec.head = h
	rec.layers = append([]capLayer(nil), base.layers...)
	rec.userVars = mergeStringMap(nil, base.userVars)
	rec.notes = append([]string(nil), base.notes...)
}

func cloneTransport(t Transport) Transport {
	out := t
	out.Vars = mergeStringMap(nil, t.Vars)
	out.VarsEnv = mergeStringMap(nil, t.VarsEnv)
	if t.Body != nil {
		out.Body = make(map[string]any, len(t.Body))
		maps.Copy(out.Body, t.Body)
	}
	return out
}

func cloneModel(m Model) Model {
	out := m
	out.Headers = mergeStringMap(nil, m.Headers)
	if m.Transport != nil {
		t := cloneTransport(*m.Transport)
		out.Transport = &t
	}
	return out
}

// clearProtocolTransport drops the protocol-specific transport fields a
// cross-protocol record does not inherit (spec §4.2).
func clearProtocolTransport(t *Transport) {
	t.Endpoint, t.StreamEndpoint, t.ModelsEndpoint, t.CountTokensEndpoint, t.Body = "", "", "", "", nil
}

// expandPreset starts from the named preset and overlays t's own fields.
func expandPreset(t Transport, presets map[string]Transport, where string) (Transport, error) {
	if t.Preset == "" {
		return t, nil
	}
	base, ok := presets[t.Preset]
	if !ok {
		return Transport{}, fmt.Errorf("%s: unknown transport preset %q", where, t.Preset)
	}
	out := cloneTransport(base)
	own := t
	own.Preset = ""
	mergeTransport(&out, own)
	out.Preset = t.Preset
	return out, nil
}

func setIfNonEmpty(dst *string, src string) {
	if src != "" {
		*dst = src
	}
}

// fold overlays one layer's record onto rec (spec §4.1, §4.2): scalars set
// if non-empty, maps key-wise, transports field-wise after preset expansion,
// the cross-protocol rule, and inherit_models = false.
func (rec *record) fold(src Provider, tag string, presets map[string]Transport) error {
	h := &rec.head
	where := "providers." + src.ID
	layer := capLayer{tag: tag, owner: src.ID, provider: src.Caps, rows: map[string]Model{}}
	if src.Protocol != "" && h.Protocol != "" && src.Protocol != h.Protocol {
		clearProtocolTransport(&h.Transport)
		layer.resetFields = true
	}
	if src.InheritModels != nil && !*src.InheritModels {
		h.Models = map[string]Model{}
		for i := range rec.layers {
			rec.layers[i].rows = nil
		}
	}
	t, err := expandPreset(src.Transport, presets, where)
	if err != nil {
		return err
	}
	if tag == LayerConfig {
		rec.userVars = mergeStringMap(rec.userVars, t.Vars)
		t.Vars = nil
	}
	mergeTransport(&h.Transport, t)
	setIfNonEmpty(&h.Name, src.Name)
	setIfNonEmpty(&h.Doc, src.Doc)
	setIfNonEmpty(&h.Protocol, src.Protocol)
	setIfNonEmpty(&h.Surface, src.Surface)
	setIfNonEmpty(&h.Family, src.Family)
	setIfNonEmpty(&h.APIKey, src.APIKey)
	setIfNonEmpty(&h.DefaultModel, src.DefaultModel)
	setIfNonEmpty(&h.CheapModel, src.CheapModel)
	if src.Implicit != nil {
		h.Implicit = src.Implicit
	}
	if src.InheritModels != nil {
		h.InheritModels = src.InheritModels
	}
	if src.APIKeyEnv != nil {
		h.APIKeyEnv = append([]string{}, src.APIKeyEnv...)
	}
	h.Headers = mergeStringMap(h.Headers, src.Headers)
	h.CredentialHeaders = mergeStringMap(h.CredentialHeaders, src.CredentialHeaders)
	rec.notes = append(rec.notes, src.notes...)
	for id, m := range src.Models {
		merged, err := foldModel(h.Models[id], m, presets, fmt.Sprintf("%s.models.%q", where, id))
		if err != nil {
			return err
		}
		h.Models[id] = merged
		layer.rows[id] = m
	}
	rec.layers = append(rec.layers, layer)
	return nil
}

// foldModel overlays one layer's row onto the merged row head. Hidden comes
// from the layer that introduces the row and clears when any layer supplies
// a protocol or transport (spec §6.1).
func foldModel(prev, src Model, presets map[string]Transport, where string) (Model, error) {
	if prev.ID == "" {
		prev = Model{ID: src.ID, Hidden: src.Hidden}
	}
	setIfNonEmpty(&prev.WireID, src.WireID)
	setIfNonEmpty(&prev.AliasOf, src.AliasOf)
	setIfNonEmpty(&prev.Family, src.Family)
	setIfNonEmpty(&prev.Protocol, src.Protocol)
	setIfNonEmpty(&prev.Surface, src.Surface)
	setIfNonEmpty(&prev.Status, src.Status)
	prev.Headers = mergeStringMap(prev.Headers, src.Headers)
	if src.Transport != nil {
		t, err := expandPreset(*src.Transport, presets, where)
		if err != nil {
			return Model{}, err
		}
		if prev.Transport == nil {
			prev.Transport = &Transport{}
		} else {
			c := cloneTransport(*prev.Transport)
			prev.Transport = &c
		}
		mergeTransport(prev.Transport, t)
	}
	if src.Protocol != "" || src.Transport != nil {
		prev.Hidden = false
	}
	return prev, nil
}

// validateRecord applies the load-time rules that need the merged record:
// fields keys against the resolved protocol (own layers only; keys inherited
// from a base on another protocol are ignored) and alias targets (spec §4.2,
// §10).
func (r *Registry) validateRecord(rec *record) error {
	where := "providers." + rec.name
	for _, layer := range rec.layers {
		if layer.owner != rec.name {
			continue
		}
		if err := ValidateFields(layer.provider.Fields, rec.head.Protocol, layer.tag+" "+where); err != nil {
			return err
		}
		for id, row := range layer.rows {
			if isGlob(id) {
				continue
			}
			proto := rec.head.Models[id].Protocol
			if proto == "" {
				proto = rec.head.Protocol
			}
			if err := ValidateFields(row.Caps.Fields, proto, fmt.Sprintf("%s %s.models.%q", layer.tag, where, id)); err != nil {
				return err
			}
		}
	}
	for _, id := range sortedKeys(rec.head.Models) {
		row := rec.head.Models[id]
		if row.AliasOf == "" || isGlob(id) {
			continue
		}
		target, err := r.aliasTarget(rec, row.AliasOf)
		switch {
		case err == nil && target.AliasOf != "":
			return fmt.Errorf("%s.models.%q: alias_of %q is itself an alias (aliases are one hop)", where, id, row.AliasOf)
		case err != nil && rec.curated:
			row.Hidden = true
			rec.head.Models[id] = row
			r.warnings = append(r.warnings, fmt.Sprintf("%s.models.%q: dangling alias %q (row hidden)", where, id, row.AliasOf))
		case err != nil:
			return fmt.Errorf("%s.models.%q: %w", where, id, err)
		}
	}
	return nil
}

// aliasTarget resolves an alias_of reference: an exact row of the same
// record first, else "provider-id/id" against the curated registry.
func (r *Registry) aliasTarget(rec *record, ref string) (Model, error) {
	if m, ok := rec.head.Models[ref]; ok && !isGlob(ref) {
		return m, nil
	}
	if i := strings.Index(ref, "/"); i > 0 {
		if prov, ok := r.curated[ref[:i]]; ok {
			if m, ok := prov.head.Models[ref[i+1:]]; ok {
				return m, nil
			}
			return Model{}, fmt.Errorf("alias_of %q: no row %q on %s", ref, ref[i+1:], ref[:i])
		}
	}
	return Model{}, fmt.Errorf("alias_of %q does not name an existing row", ref)
}

var placeholderRe = regexp.MustCompile(`\{([A-Z][A-Z0-9_]*)\}`)

// expandTemplate substitutes {VAR} placeholders (uppercase names only;
// {model} is left for request time) and returns the names it could not resolve.
func expandTemplate(tpl string, lookup func(string) (string, bool)) (string, []string) {
	var missing []string
	out := placeholderRe.ReplaceAllStringFunc(tpl, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := lookup(name); ok {
			return v
		}
		missing = append(missing, name)
		return m
	})
	return out, missing
}

// varLookup is the spec §9.1 order: the user layer's vars, then the
// environment through vars_env, then the curated and upstream defaults.
func (r *Registry) varLookup(rec *record) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if v, ok := rec.userVars[name]; ok {
			if expanded, missing := expandEnv(v, r.env); len(missing) == 0 && expanded != "" {
				return expanded, true
			}
		}
		if envName, ok := rec.head.Transport.VarsEnv[name]; ok {
			if v, ok := r.env(envName); ok && v != "" {
				return v, true
			}
		}
		if v, ok := rec.head.Transport.Vars[name]; ok && v != "" {
			return v, true
		}
		return "", false
	}
}

// resolveBaseURL substitutes t.BaseURL for rec, applying the transport's
// host rule (spec §9.1). missing lists unresolved variables; warnings carry
// host-rule failures.
func (r *Registry) resolveBaseURL(rec *record, t Transport) (string, []string, []string) {
	lookup := r.varLookup(rec)
	var warnings []string
	switch t.HostRule {
	case HostRuleOllamaHost:
		inner := lookup
		lookup = func(name string) (string, bool) {
			if name != "OLLAMA_HOST" {
				return inner(name)
			}
			baseURL, _ := r.env("OLLAMA_BASE_URL")
			host, _ := inner("OLLAMA_HOST")
			u, err := resolveOllamaHost(baseURL, host)
			if err != nil {
				warnings = append(warnings, err.Error())
				return "", false
			}
			return u, true
		}
	case HostRuleVertexLocation:
		inner := lookup
		lookup = func(name string) (string, bool) {
			if name != "GOOGLE_VERTEX_HOST" {
				return inner(name)
			}
			if v, ok := inner("GOOGLE_VERTEX_HOST"); ok {
				return v, true
			}
			loc, ok := inner("GOOGLE_VERTEX_LOCATION")
			if !ok {
				return "", false
			}
			return vertexHost(loc), true
		}
	}
	url, missing := expandTemplate(t.BaseURL, lookup)
	return url, missing, warnings
}

// computeHidden applies spec §4's rule after the merge, against the
// environment: no registered protocol or no resolvable base URL hides a
// provider; rows keep the flag foldModel computed.
func (r *Registry) computeHidden(rec *record) {
	hidden := rec.head.Protocol == "" || !protocols[rec.head.Protocol]
	if !hidden {
		url, missing, _ := r.resolveBaseURL(rec, rec.head.Transport)
		hidden = url == "" || len(missing) > 0
	}
	rec.head.Hidden = hidden
}

// ProviderIDs lists the curated registry ids, sorted.
func (r *Registry) ProviderIDs() []string { return sortedKeys(r.curated) }

// Provider returns the merged curated record for a registry id, with Hidden
// computed against the environment.
func (r *Registry) Provider(id string) (Provider, bool) {
	rec, ok := r.curated[id]
	if !ok {
		return Provider{}, false
	}
	return rec.head, true
}

// UserLayerNote describes where the user layer came from ("user layer:
// none (EVENER_PROVIDERS_CONFIG is empty)", spec §14.1).
func (r *Registry) UserLayerNote() string { return r.userNote }

// Warnings returns load-level warnings (curated dangling aliases, …).
func (r *Registry) Warnings() []string { return append([]string(nil), r.warnings...) }

// Catalog reports which upstream layer is in use and its fetch metadata.
func (r *Registry) Catalog() (string, Meta) { return r.catalogTag, r.catalogMeta }

// instance and liveListing are filled in by the instances (Task 10) and
// resolve (Task 8) files.
type instance struct{ rec *record }              //nolint:unused
type liveListing struct{ rows map[string]Model } //nolint:unused
