package appsource

import (
	"context"
	"errors"
	"maps"
	"slices"

	"primeradiant.com/evener/appwire"
)

// HostCapabilities is what one probe learns about a remote host. AppWire
// supplies the launch layer, models, plugins, auth, and instances. The
// component-04 preflight supplies ProtocolVersion, HubVersion, OS, Arch, and
// the peer's advertised Features, because the remote hub rejects a second
// initialize on the already-initialized SSH channel and no other wire call
// reports them.
type HostCapabilities struct {
	ProtocolVersion string
	HubVersion      string
	HubSourceID     string // remoteHubNamespace ("local")
	Features        appwire.FeatureSet
	OS, Arch        string // component-04 preflight, not AppWire
	LaunchGlobal    appwire.LaunchConfigLayer
	Models          appwire.ModelListResponse
	Plugins         appwire.PluginListResponse
	Auth            appwire.AuthListResponse
	Instances       appwire.InstanceListResponse
	Roots           []string // from host config entry
}

// CapabilitySource reports what a source knows about its host. It is separate
// from Source so a consumer can probe a remote hub without depending on the
// thread/model read surface.
type CapabilitySource interface {
	HostCapabilities(context.Context) (HostCapabilities, error)
}

var _ CapabilitySource = (*RemoteHubSource)(nil)

// HostFacts is the preflight half of HostCapabilities: the facts AppWire cannot
// supply on an already-initialized connection. Component 04 produces them.
type HostFacts struct {
	ProtocolVersion string
	HubVersion      string
	OS, Arch        string
	Features        appwire.FeatureSet
}

// HostFactsFunc returns the preflight facts for a remote host's AppWire
// connection. client is the exact generation HostCapabilities resolved and ran
// its wire reads on, so an implementation must answer from that same generation
// (or report that it changed) instead of racing a component-04 reconnect and
// mixing two connections' worth of state into one snapshot.
type HostFactsFunc func(ctx context.Context, host string, client *appwire.Client) (HostFacts, error)

// HostHandshakeFunc returns the InitializeResponse the named remote host's
// channel captured when it attached, and reports false when no live channel is
// installed or when the installed channel is not the one client belongs to. It
// never dials. The capability probe reads ProtocolVersion, SourceID, and
// Features through it: those three are handshake-only, because the
// connection-scoped initialize cannot be re-run on the already-initialized
// channel component 04 handed over and no other wire call reports them. The
// handshake's ServerInfo is deliberately not consumed — its Version is the
// hub's package constant, not its running build, so HubVersion stays
// preflight/buildinfo-owned. client is the exact generation the probe resolved
// and ran its other reads on, so an implementation must answer from that
// generation or report false rather than race a reconnect; a false answer makes
// the probe refuse with the typed SessionUnavailable and cache nothing. In
// production it is backed by the remoteHostHandshakeForChannel closure over
// sshconn.Manager.ChannelIfAttached, with the channel-identity check at the call
// site rather than inside an accessor.
type HostHandshakeFunc func(host string, client *appwire.Client) (appwire.InitializeResponse, bool)

// remoteHubProbe is one successful probe cached against the client it ran on.
// Keeping the client pointer lets HostCapabilities re-probe automatically when
// a component-04 reconnect hands back a new client.
type remoteHubProbe struct {
	client *appwire.Client
	caps   HostCapabilities
}

// HostCapabilities probes the remote hub over the current client and assembles
// its capabilities. A repeat call on the same client returns the cached result
// without any wire traffic; probeMu guards only that cache and the facts seam,
// never the wire calls, so a slow or unresponsive hub cannot block another
// caller on network I/O and each caller's context governs its own probe.
//
// The five AppWire reads are the hub-scope surfaces the controller needs a
// snapshot of; every one runs on the exact client the cache is keyed against,
// so a component-04 reconnect that hands back a new client re-probes.
func (s *RemoteHubSource) HostCapabilities(ctx context.Context) (HostCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return HostCapabilities{}, s.mapCallError(err)
	}
	client, err := s.resolveClient(ctx)
	if err != nil {
		return HostCapabilities{}, s.mapCallError(err)
	}

	if caps, ok := s.cachedCapabilities(client); ok {
		return caps, nil
	}

	// Snapshot the facts seam under the lock SetHostFacts writes it under; the
	// probe below runs without probeMu so it holds no lock across wire I/O.
	s.probeMu.Lock()
	factsFn := s.facts
	handshakeFn := s.handshake
	s.probeMu.Unlock()

	var caps HostCapabilities
	// The global layer is a function of the hub's state root, not a working
	// directory, but hubLaunchController.GetLayer canonicalizes params.CWD
	// before it dispatches on the layer, and CanonicalizeDir rejects the empty
	// string. Send "/" — the same placeholder the web and mobile clients use —
	// so a real hub accepts the read.
	if err := s.callOn(ctx, client, appwire.MethodEvenerLaunchGetLayer, appwire.LaunchConfigGetLayerParams{CWD: "/", Layer: "global"}, &caps.LaunchGlobal); err != nil {
		return HostCapabilities{}, err
	}
	if err := s.callOn(ctx, client, appwire.MethodModelList, appwire.ModelListParams{}, &caps.Models); err != nil {
		return HostCapabilities{}, err
	}
	if err := s.callOn(ctx, client, appwire.MethodEvenerPluginList, appwire.EmptyParams{}, &caps.Plugins); err != nil {
		return HostCapabilities{}, err
	}
	if err := s.callOn(ctx, client, appwire.MethodEvenerAuthList, appwire.EmptyParams{}, &caps.Auth); err != nil {
		return HostCapabilities{}, err
	}
	if err := s.callOn(ctx, client, appwire.MethodEvenerInstanceList, appwire.EmptyParams{}, &caps.Instances); err != nil {
		// evener/instance/list is registered only when the hub has a providers
		// config (no-user-layer mode registers no instance surface). A hub in
		// that supported configuration is healthy but answers
		// CodeMethodNotFound; that is not a probe failure, so leave
		// caps.Instances empty and keep collecting the rest of the snapshot.
		// Every other failure still aborts.
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeMethodNotFound {
			return HostCapabilities{}, err
		}
	}

	// No facts seam means no preflight was wired; the preflight-owned fields
	// stay zero-valued rather than being guessed from the wire.
	if factsFn != nil {
		facts, err := factsFn(ctx, s.id, client)
		if err != nil {
			return HostCapabilities{}, s.mapCallError(err)
		}
		caps.ProtocolVersion = facts.ProtocolVersion
		caps.HubVersion = facts.HubVersion
		caps.OS = facts.OS
		caps.Arch = facts.Arch
		caps.Features = facts.Features
	}
	// The attach handshake carries the connection's own ProtocolVersion,
	// SourceID, and Features. When component 04 exposed it, those three are
	// authoritative — they describe this exact generation — while OS/Arch and
	// HubVersion stay preflight-owned: the handshake's ServerInfo.Version is the
	// hub's package constant, not its running build, so the facts seam's
	// buildinfo value is the single version source. Without the seam the
	// preflight-owned fields above stand and the remote namespace is fixed.
	//
	// A false answer means the seam could not answer from the generation the
	// probe resolved — no live channel, or one that is not this client's — so
	// refuse with the typed SessionUnavailable and cache nothing rather than
	// serve a snapshot that pairs this client's wire reads with another
	// generation's handshake facts.
	if handshakeFn != nil {
		hs, ok := handshakeFn(s.id, client)
		if !ok {
			return HostCapabilities{}, appwire.SessionUnavailable("remote hub unavailable: " + s.id)
		}
		caps.ProtocolVersion = hs.ProtocolVersion
		caps.HubSourceID = hs.SourceID
		caps.Features = hs.Features
	}
	if caps.HubSourceID == "" {
		caps.HubSourceID = remoteHubNamespace
	}
	caps.Roots = append([]string(nil), s.roots...)

	s.probeMu.Lock()
	s.probe = &remoteHubProbe{client: client, caps: caps.clone()}
	s.probeMu.Unlock()
	return caps.clone(), nil
}

// cachedCapabilities returns the probe cached against client, if any. The
// cache is keyed on the client pointer so a reconnect's new client forces a
// fresh probe; a stale publish is therefore self-correcting on the next call.
func (s *RemoteHubSource) cachedCapabilities(client *appwire.Client) (HostCapabilities, bool) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probe != nil && s.probe.client == client {
		return s.probe.caps.clone(), true
	}
	return HostCapabilities{}, false
}

// clone returns a deep copy of the snapshot: every slice, map, and pointer
// scalar a caller could mutate is duplicated — down through a provider
// descriptor's Setup entry — so neither a cache hit nor a freshly published
// probe shares mutable state with the stored value. Without it a caller editing
// a returned Models.Data entry, Roots element, or a *LaunchGlobal scalar in
// place would corrupt the cached probe for every later caller.
func (c HostCapabilities) clone() HostCapabilities {
	out := c
	out.LaunchGlobal = cloneLaunchConfigLayer(c.LaunchGlobal)
	out.Models = cloneModelListResponse(c.Models)
	out.Plugins = appwire.PluginListResponse{Plugins: slices.Clone(c.Plugins.Plugins)}
	out.Auth = appwire.AuthListResponse{Providers: cloneAuthStatusResponses(c.Auth.Providers)}
	out.Instances = cloneInstanceListResponse(c.Instances)
	out.Roots = slices.Clone(c.Roots)
	return out
}

// ptrClone duplicates the scalar behind p so a clone shares no pointer with the
// original. A nil pointer stays nil.
func ptrClone[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneLaunchConfigLayer(l appwire.LaunchConfigLayer) appwire.LaunchConfigLayer {
	out := l
	out.Schema = ptrClone(l.Schema)
	out.SandboxNet = ptrClone(l.SandboxNet)
	out.MaxRounds = ptrClone(l.MaxRounds)
	out.MaxSubagentDepth = ptrClone(l.MaxSubagentDepth)
	out.MaxConcurrentDelegateTurns = ptrClone(l.MaxConcurrentDelegateTurns)
	out.MaxRetainedTerminal = ptrClone(l.MaxRetainedTerminal)
	out.NoProjectPrompts = ptrClone(l.NoProjectPrompts)
	out.NonInteractive = ptrClone(l.NonInteractive)
	out.AppReplaySize = ptrClone(l.AppReplaySize)
	out.Verbose = ptrClone(l.Verbose)
	out.APILog = ptrClone(l.APILog)
	out.SkillsDirs = slices.Clone(l.SkillsDirs)
	out.PluginDirs = slices.Clone(l.PluginDirs)
	out.MCPConfigs = slices.Clone(l.MCPConfigs)
	out.SystemPromptAppend = slices.Clone(l.SystemPromptAppend)
	out.ModelFallbacks = slices.Clone(l.ModelFallbacks)
	if l.EnabledPlugins != nil {
		enabled := slices.Clone(*l.EnabledPlugins)
		out.EnabledPlugins = &enabled
	}
	out.MCPs = slices.Clone(l.MCPs)
	for i := range out.MCPs {
		out.MCPs[i].Args = slices.Clone(out.MCPs[i].Args)
	}
	out.Env = maps.Clone(l.Env)
	return out
}

func cloneModelListResponse(m appwire.ModelListResponse) appwire.ModelListResponse {
	out := m
	out.Data = cloneModelDescriptors(m.Data)
	out.Diagnostics = slices.Clone(m.Diagnostics)
	out.Recent = cloneModelDescriptors(m.Recent)
	return out
}

func cloneModelDescriptors(in []appwire.ModelDescriptor) []appwire.ModelDescriptor {
	out := slices.Clone(in)
	for i := range out {
		out[i].ContextWindow = ptrClone(out[i].ContextWindow)
		out[i].MaxInputTokens = ptrClone(out[i].MaxInputTokens)
		out[i].SupportsTools = ptrClone(out[i].SupportsTools)
		out[i].SupportsVision = ptrClone(out[i].SupportsVision)
		out[i].MaxOutputTokens = ptrClone(out[i].MaxOutputTokens)
		out[i].SupportsWebSearch = ptrClone(out[i].SupportsWebSearch)
		out[i].SupportsReasoning = ptrClone(out[i].SupportsReasoning)
		out[i].InputCostPerMillion = ptrClone(out[i].InputCostPerMillion)
		out[i].OutputCostPerMillion = ptrClone(out[i].OutputCostPerMillion)
		out[i].ReasoningEffortLevels = slices.Clone(out[i].ReasoningEffortLevels)
		out[i].Warnings = slices.Clone(out[i].Warnings)
	}
	return out
}

func cloneAuthStatusResponses(in []appwire.AuthStatusResponse) []appwire.AuthStatusResponse {
	out := slices.Clone(in)
	for i := range out {
		out[i].AuthModes = slices.Clone(out[i].AuthModes)
	}
	return out
}

// cloneInstanceEntry duplicates every mutable field one instance entry owns: its
// Vars map, AuthModes and Warnings slices, and Models inventory. Both
// InstanceListResponse.Instances and a provider descriptor's Setup carry an
// entry, and Setup carries one the caller can write through, so the field list
// lives here once instead of being repeated per call site with a field left out
// of one of them.
//
// InstanceModelEntry needs no per-row clone: it holds only a string ID and a
// bool Disabled, so copying the slice is a complete copy of it.
func cloneInstanceEntry(e appwire.InstanceEntry) appwire.InstanceEntry {
	out := e
	out.Vars = maps.Clone(e.Vars)
	out.AuthModes = slices.Clone(e.AuthModes)
	out.Warnings = slices.Clone(e.Warnings)
	out.Models = slices.Clone(e.Models)
	return out
}

func cloneInstanceListResponse(in appwire.InstanceListResponse) appwire.InstanceListResponse {
	out := in
	out.Instances = slices.Clone(in.Instances)
	for i := range out.Instances {
		out.Instances[i] = cloneInstanceEntry(out.Instances[i])
	}
	out.AvailableProviders = slices.Clone(in.AvailableProviders)
	for i := range out.AvailableProviders {
		out.AvailableProviders[i].VarsEnv = slices.Clone(out.AvailableProviders[i].VarsEnv)
		out.AvailableProviders[i].Vars = maps.Clone(out.AvailableProviders[i].Vars)
		out.AvailableProviders[i].APIKeyEnv = slices.Clone(out.AvailableProviders[i].APIKeyEnv)
		out.AvailableProviders[i].AuthModes = slices.Clone(out.AvailableProviders[i].AuthModes)
		if setup := out.AvailableProviders[i].Setup; setup != nil {
			cloned := cloneInstanceEntry(*setup)
			out.AvailableProviders[i].Setup = &cloned
		}
	}
	out.Diagnostics = slices.Clone(in.Diagnostics)
	return out
}
