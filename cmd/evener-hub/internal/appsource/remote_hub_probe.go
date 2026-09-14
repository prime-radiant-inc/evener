package appsource

import (
	"context"

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

// HostFactsFunc returns the preflight facts for a remote host.
type HostFactsFunc func(ctx context.Context, host string) (HostFacts, error)

// remoteHubProbe is one successful probe cached against the client it ran on.
// Keeping the client pointer lets HostCapabilities re-probe automatically when
// a component-04 reconnect hands back a new client.
type remoteHubProbe struct {
	client *appwire.Client
	caps   HostCapabilities
}

// HostCapabilities probes the remote hub over the current client and assembles
// its capabilities. The whole probe is serialized under probeMu (probes are
// rare, so a plain lock is simpler and safer than a per-client cache), and a
// repeat call on the same client returns the cached result without any wire
// traffic.
//
// The five AppWire reads are the hub-scope surfaces the controller needs a
// snapshot of; every one runs on the exact client the cache is keyed against.
func (s *RemoteHubSource) HostCapabilities(ctx context.Context) (HostCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return HostCapabilities{}, s.mapCallError(err)
	}
	client, err := s.client(ctx, s.id)
	if err != nil {
		return HostCapabilities{}, s.mapCallError(err)
	}

	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probe != nil && s.probe.client == client {
		return s.probe.caps, nil
	}

	var caps HostCapabilities
	if err := s.callOn(ctx, client, appwire.MethodEvenerLaunchGetLayer, appwire.LaunchConfigGetLayerParams{CWD: "", Layer: "global"}, &caps.LaunchGlobal); err != nil {
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
		return HostCapabilities{}, err
	}

	// No facts seam means no preflight was wired; the preflight-owned fields
	// stay zero-valued rather than being guessed from the wire.
	if s.facts != nil {
		facts, err := s.facts(ctx, s.id)
		if err != nil {
			return HostCapabilities{}, err
		}
		caps.ProtocolVersion = facts.ProtocolVersion
		caps.HubVersion = facts.HubVersion
		caps.OS = facts.OS
		caps.Arch = facts.Arch
		caps.Features = facts.Features
	}
	caps.HubSourceID = remoteHubNamespace
	caps.Roots = append([]string(nil), s.roots...)

	s.probe = &remoteHubProbe{client: client, caps: caps}
	return caps, nil
}
