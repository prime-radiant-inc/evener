package registry

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
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
	snapshot    []byte
	overlay     []byte
	logf        func(format string, args ...any)
	noCache     bool
}

type parsedCatalog struct {
	providers []Provider
	meta      Meta
}

var loadEmbeddedCatalog = sync.OnceValues(func() (parsedCatalog, error) {
	raw, meta, err := EmbeddedSnapshot()
	if err != nil {
		return parsedCatalog{}, err
	}
	providers, err := FromModelsDev(raw)
	if err != nil {
		return parsedCatalog{}, err
	}
	return parsedCatalog{providers: providers, meta: meta}, nil
})

var loadEmbeddedOverlay = sync.OnceValues(func() (*Layer, error) {
	return ParseOverlay(EmbeddedOverlay())
})

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
// Load reads but does not recursively snapshot the supplied reference-valued
// fields, so callers must not mutate them while the loaded Registry is in use.
func WithInstances(instances map[string]Provider) Option {
	return func(o *options) { o.instances = instances }
}

// WithSnapshot replaces the embedded models.dev JSON (tests).
func WithSnapshot(raw []byte) Option { return func(o *options) { o.snapshot = raw } }

// WithoutCache ignores the runtime catalog cache so the load reflects the
// snapshot alone: the refresh validation checks the candidate body, and the
// snapshot report reads what ships in the binary.
func WithoutCache() Option { return func(o *options) { o.noCache = true } }

// WithOverlay replaces the embedded curated overlay (tests).
func WithOverlay(data []byte) Option { return func(o *options) { o.overlay = data } }

// WithLog receives the one-line messages a failed background refresh
// produces (default: log.Printf).
func WithLog(logf func(format string, args ...any)) Option { return func(o *options) { o.logf = logf } }

// Registry is the loaded, layered provider registry (spec §5). It is
// immutable after Load except for the live listings Resolve consults.
type Registry struct {
	presets        map[string]Transport
	defaultOrder   []string
	topGlobs       map[string]map[string]Model // layer tag → glob rows
	curated        map[string]*record          // registry ids (layers 1–3)
	explicit       map[string]*record          // user-layer and injected instances
	userDefault    string
	userNote       string
	env            func(string) (string, bool)
	creds          CredentialSource
	stateRoot      string
	catalogTag     string
	catalogMeta    Meta
	warnings       []string
	instances      map[string]*instance
	liveMu         sync.RWMutex
	live           map[string]liveListing
	refreshStarted bool
	refreshDone    chan struct{}
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

	ownBaseURL   string   // a literal base_url set by the config layer (endpoint stop, spec §10)
	ownAPIKeyEnv []string // api_key_env set by the config layer (survives the endpoint stop)
}

// capLayer is one layer's contribution to a record. own is true for the
// layers the record itself contributed (false for layers copied from its
// base chain), which is what "own layers" means in spec §10.
type capLayer struct {
	tag         string
	owner       string
	own         bool
	provider    Caps
	rows        map[string]Model
	resetFields bool
}

func defaultOptions() *options {
	return &options{env: os.LookupEnv, logf: log.Printf}
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
// overlay, and the user layer (spec §5). A newer runtime cache replaces the
// embedded snapshot, and a stale cache starts the background refresh unless
// the load is offline (spec §6.4).
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
	var upstream []Provider
	meta := Meta{}
	if o.snapshot == nil {
		catalog, err := loadEmbeddedCatalog()
		if err != nil {
			return nil, err
		}
		upstream, meta = catalog.providers, catalog.meta
	}
	r.catalogTag, r.catalogMeta = LayerSnapshot, meta
	if !o.noCache {
		if cachedRaw, cachedMeta, ok := readCache(o.stateRoot); ok && cachedMeta.FetchedAt.After(meta.FetchedAt) {
			candidate, err := FromModelsDev(cachedRaw)
			if err != nil {
				jsonPath, _ := cachePaths(o.stateRoot)
				r.warnings = append(r.warnings, fmt.Sprintf("ignoring corrupt catalog cache %s: %v", jsonPath, err))
			} else {
				upstream = candidate
				r.catalogTag, r.catalogMeta = LayerCache, cachedMeta
			}
		}
	}
	if o.snapshot != nil && r.catalogTag == LayerSnapshot {
		var err error
		upstream, err = FromModelsDev(o.snapshot)
		if err != nil {
			return nil, err
		}
	}
	upstreamByID := make(map[string]Provider, len(upstream))
	for _, p := range upstream {
		upstreamByID[p.ID] = p
	}
	var ov *Layer
	if o.overlay == nil {
		var err error
		ov, err = loadEmbeddedOverlay()
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		ov, err = ParseOverlay(o.overlay)
		if err != nil {
			return nil, err
		}
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
	r.computeInstances()
	if err := r.validateDefault(); err != nil {
		return nil, err
	}
	r.maybeStartRefresh(o)
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
	for i := range rec.layers {
		rec.layers[i].own = false
	}
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

func cloneProviderView(p Provider) Provider {
	out := p
	out.InheritModels = clonePointer(p.InheritModels)
	out.Implicit = clonePointer(p.Implicit)
	out.Transport = cloneTransportView(p.Transport)
	out.APIKeyEnv = slices.Clone(p.APIKeyEnv)
	out.Headers = maps.Clone(p.Headers)
	out.CredentialHeaders = maps.Clone(p.CredentialHeaders)
	out.Caps = cloneCaps(p.Caps)
	out.Models = make(map[string]Model, len(p.Models))
	for id, model := range p.Models {
		out.Models[id] = cloneModelView(model)
	}
	out.notes = slices.Clone(p.notes)
	return out
}

func cloneModelView(m Model) Model {
	out := m
	out.Headers = maps.Clone(m.Headers)
	out.Caps = cloneCaps(m.Caps)
	if m.Transport != nil {
		transport := cloneTransportView(*m.Transport)
		out.Transport = &transport
	}
	return out
}

func cloneTransportView(t Transport) Transport {
	out := t
	out.Vars = mergeStringMap(nil, t.Vars)
	out.VarsEnv = mergeStringMap(nil, t.VarsEnv)
	out.Body = cloneAnyMap(t.Body)
	return out
}

func cloneCaps(c Caps) Caps {
	out := c
	out.ContextWindow = clonePointer(c.ContextWindow)
	out.MaxOutputTokens = clonePointer(c.MaxOutputTokens)
	out.Tools = clonePointer(c.Tools)
	out.StructuredOutput = clonePointer(c.StructuredOutput)
	out.Sampling = clonePointer(c.Sampling)
	out.Reasoning = clonePointer(c.Reasoning)
	out.ReasoningControls = slices.Clone(c.ReasoningControls)
	out.EffortValues = slices.Clone(c.EffortValues)
	out.DefaultEffort = clonePointer(c.DefaultEffort)
	out.InputModalities = slices.Clone(c.InputModalities)
	out.KnowledgeCutoff = clonePointer(c.KnowledgeCutoff)
	if c.Cost != nil {
		cost := *c.Cost
		cost.Tiers = slices.Clone(c.Cost.Tiers)
		out.Cost = &cost
	}
	out.Fields = maps.Clone(c.Fields)
	out.MaxTokensField = clonePointer(c.MaxTokensField)
	out.ThinkingFormat = clonePointer(c.ThinkingFormat)
	out.ThinkingShape = clonePointer(c.ThinkingShape)
	out.ThinkingDisplay = clonePointer(c.ThinkingDisplay)
	out.ThinkingAlwaysOn = clonePointer(c.ThinkingAlwaysOn)
	out.ReasoningField = clonePointer(c.ReasoningField)
	out.ReasoningSummary = clonePointer(c.ReasoningSummary)
	out.ChatTemplateKwargs = cloneAnyMap(c.ChatTemplateKwargs)
	out.FinishReasonMap = maps.Clone(c.FinishReasonMap)
	out.CacheControl = clonePointer(c.CacheControl)
	out.CacheTTL = clonePointer(c.CacheTTL)
	out.StrictTools = clonePointer(c.StrictTools)
	out.ToolChoiceForcing = clonePointer(c.ToolChoiceForcing)
	out.MaxStopSequences = clonePointer(c.MaxStopSequences)
	out.ImageDetail = clonePointer(c.ImageDetail)
	out.ResponsesLite = clonePointer(c.ResponsesLite)
	out.AssistantAfterToolResult = clonePointer(c.AssistantAfterToolResult)
	out.ThinkingAsText = clonePointer(c.ThinkingAsText)
	out.EmptyReasoningContent = clonePointer(c.EmptyReasoningContent)
	out.StripEmptyContent = clonePointer(c.StripEmptyContent)
	out.ToolResultName = clonePointer(c.ToolResultName)
	out.ToolStream = clonePointer(c.ToolStream)
	out.SessionAffinityHeaders = clonePointer(c.SessionAffinityHeaders)
	out.MultimodalToolResults = clonePointer(c.MultimodalToolResults)
	out.WebSearch = clonePointer(c.WebSearch)
	return out
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneAnyValue(value)
	}
	return out
}

func cloneAnyValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneAnyMap(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneAnyValue(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(value))
		for i, item := range value {
			out[i] = cloneAnyMap(item)
		}
		return out
	case []string:
		return slices.Clone(value)
	case map[string]string:
		return maps.Clone(value)
	default:
		return value
	}
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
	layer := capLayer{tag: tag, owner: src.ID, own: true, provider: src.Caps, rows: map[string]Model{}}
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
		rec.ownBaseURL = t.BaseURL
		if src.APIKeyEnv != nil {
			rec.ownAPIKeyEnv = append([]string{}, src.APIKeyEnv...)
		}
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
	for _, id := range sortedKeys(src.Models) {
		m := src.Models[id]
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
		if !layer.own {
			continue
		}
		// A record with no protocol (the upstream entry vanished, or an npm
		// the converter hides) is Hidden and has no prunable set to check
		// provider-level fields against; rows that declare their own
		// protocol are still checked.
		if rec.head.Protocol != "" {
			if err := ValidateFields(layer.provider.Fields, rec.head.Protocol, layer.tag+" "+where); err != nil {
				return err
			}
		}
		for _, id := range sortedKeys(layer.rows) {
			row := layer.rows[id]
			if isGlob(id) {
				continue
			}
			proto := rec.head.Models[id].Protocol
			if proto == "" {
				proto = rec.head.Protocol
			}
			if proto == "" {
				continue
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
		case err != nil && rec.aliasFromConfig(id):
			return fmt.Errorf("%s.models.%q: %w", where, id, err)
		case err != nil:
			// A dangling alias contributed by a curated layer (upstream dropped
			// the target) degrades to a hidden row, on the curated record and
			// on every instance that inherits it (spec §4.2).
			row.Hidden = true
			rec.head.Models[id] = row
			r.warnings = append(r.warnings, fmt.Sprintf("%s.models.%q: dangling alias %q (row hidden)", where, id, row.AliasOf))
		}
	}
	return nil
}

// aliasFromConfig reports whether the user layer set this row's alias_of.
func (rec *record) aliasFromConfig(id string) bool {
	for _, l := range rec.layers {
		if l.tag != LayerConfig {
			continue
		}
		if m, ok := l.rows[id]; ok && m.AliasOf != "" {
			return true
		}
	}
	return false
}

// aliasTarget resolves an alias_of reference: an exact row of the same
// record first, else "provider-id/id" against the curated registry. A glob
// pattern never names a target, on either side of the slash.
func (r *Registry) aliasTarget(rec *record, ref string) (Model, error) {
	if m, ok := rec.head.Models[ref]; ok && !isGlob(ref) {
		return m, nil
	}
	if i := strings.Index(ref, "/"); i > 0 {
		if prov, ok := r.curated[ref[:i]]; ok {
			if m, ok := prov.head.Models[ref[i+1:]]; ok && !isGlob(ref[i+1:]) {
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

// varLookupWith is the spec §9.1 variable order with a warnings sink: the
// user layer's vars, then the environment through vars_env, then the curated
// and upstream defaults. A user `vars` entry whose $ENV reference is unset
// warns and resolves to nothing instead of falling through to vars_env or
// the curated default, so the URL never silently uses the value the user
// meant to replace (spec §10).
func (r *Registry) varLookupWith(rec *record, warn func(string)) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if v, ok := rec.userVars[name]; ok {
			expanded, missing := expandEnv(v, r.env)
			for _, m := range missing {
				warn("unresolved variable " + m)
			}
			switch {
			case len(missing) > 0:
				return "", false
			case expanded != "":
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

// varLookup is varLookupWith for the callers that have no warnings channel
// (the hidden computation and the endpoint stop).
func (r *Registry) varLookup(rec *record) func(string) (string, bool) {
	return r.varLookupWith(rec, func(string) {})
}

// resolveBaseURLWith substitutes t.BaseURL for rec using lookup, applying
// the transport's host rule (spec §9.1). missing lists unresolved
// variables; warnings carry host-rule failures.
func (r *Registry) resolveBaseURLWith(rec *record, t Transport, lookup func(string) (string, bool)) (string, []string, []string) {
	return r.resolveBaseURLVia(t, lookup, r.env)
}

// resolveBaseURLVia is resolveBaseURLWith with the environment injectable:
// the ollama-host rule reads OLLAMA_BASE_URL from the environment directly
// rather than through the variable lookup, so the canonical first-party
// resolution (firstPartyEndpoint, instances.go) passes an empty env to keep
// a live override out of the canonical URL.
func (r *Registry) resolveBaseURLVia(t Transport, lookup, env func(string) (string, bool)) (string, []string, []string) {
	var warnings []string
	switch t.HostRule {
	case HostRuleOllamaHost:
		inner := lookup
		lookup = func(name string) (string, bool) {
			if name != "OLLAMA_HOST" {
				return inner(name)
			}
			baseURL, _ := env("OLLAMA_BASE_URL")
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
			if !validVertexLocation(loc) {
				warnings = append(warnings, fmt.Sprintf("invalid GOOGLE_VERTEX_LOCATION %q: a Vertex location is a single hostname label (letters, digits, and interior hyphens, like us-central1)", loc))
				return "", false
			}
			return vertexHost(loc), true
		}
	}
	url, missing := expandTemplate(t.BaseURL, lookup)
	return url, missing, warnings
}

// resolveBaseURL substitutes t.BaseURL for rec with the spec §9.1 variable
// order (user vars, environment, curated defaults). Its warnings carry the
// host-rule failures and the unresolved $ENV references of user `vars`.
func (r *Registry) resolveBaseURL(rec *record, t Transport) (string, []string, []string) {
	var varWarnings []string
	url, missing, warnings := r.resolveBaseURLWith(rec, t, r.varLookupWith(rec, func(w string) { varWarnings = append(varWarnings, w) }))
	return url, missing, append(warnings, varWarnings...)
}

// defaultVarLookup consults only the curated and upstream defaults — no user
// vars, no environment (spec §10's "after substituting the curated defaults").
func (r *Registry) defaultVarLookup(rec *record) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := rec.head.Transport.Vars[name]
		return v, ok && v != ""
	}
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

// Provider returns an independently owned merged curated record for a registry
// id, with Hidden computed against the environment. The head carries no
// capabilities: those live in the record's layers, which only Resolve replays.
func (r *Registry) Provider(id string) (Provider, bool) {
	rec, ok := r.curated[id]
	if !ok {
		return Provider{}, false
	}
	return cloneProviderView(rec.head), true
}

// UserLayerNote describes where the user layer came from ("user layer:
// none (EVENER_PROVIDERS_CONFIG is empty)", spec §14.1).
func (r *Registry) UserLayerNote() string { return r.userNote }

// Warnings returns load-level warnings (curated dangling aliases, …).
func (r *Registry) Warnings() []string { return append([]string(nil), r.warnings...) }

// Catalog reports which upstream layer is in use and its fetch metadata.
func (r *Registry) Catalog() (string, Meta) { return r.catalogTag, r.catalogMeta }

// RefreshStarted reports whether Load started a background refresh.
func (r *Registry) RefreshStarted() bool { return r.refreshStarted }

// WaitRefresh blocks until the background refresh (if any) has finished.
func (r *Registry) WaitRefresh() {
	if r.refreshDone != nil {
		<-r.refreshDone
	}
}

// maybeStartRefresh starts the background models.dev refresh (spec §6.4)
// unless offline: EVENER_OFFLINE=1, an explicit WithOffline(true), or —
// under go test — no injected fetcher.
func (r *Registry) maybeStartRefresh(o *options) {
	offline := false
	switch {
	case o.offline != nil:
		offline = *o.offline
	case testing.Testing():
		offline = o.fetcher == nil
	}
	if v, _ := o.env("EVENER_OFFLINE"); v == "1" {
		offline = true
	}
	if offline {
		return
	}
	if _, meta, ok := readCache(o.stateRoot); ok && time.Since(meta.FetchedAt) < cacheMaxAge {
		return
	}
	fetcher := o.fetcher
	if fetcher == nil {
		fetcher = HTTPFetcher(&http.Client{Timeout: 60 * time.Second})
	}
	r.refreshStarted = true
	r.refreshDone = make(chan struct{})
	// The sanity floors compare against the embedded snapshot (spec §6.4);
	// o.snapshot is nil in production and the injected snapshot in tests.
	stateRoot, logf, baseline := o.stateRoot, o.logf, o.snapshot
	go func() {
		defer close(r.refreshDone)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := Refresh(ctx, RefreshOptions{StateRoot: stateRoot, Fetcher: fetcher, Baseline: baseline}); err != nil {
			logf("models.dev refresh: %v (keeping the previous catalog)", err)
		}
	}()
}
