package llm

import (
	"fmt"
	"sync"

	"primeradiant.com/serf/llm/providercfg"
)

// InstanceAdapterFactory constructs a ProviderAdapter for one provider instance.
// The factory receives the full InstanceConfig and the global StateHome (used by
// OAuth-backed adapters such as openai). It is registered per (type, apiStyle) pair.
type InstanceAdapterFactory func(inst providercfg.InstanceConfig, stateHome string) (ProviderAdapter, error)

var (
	instanceFactoriesMu sync.Mutex
	instanceFactories   = map[instanceFactoryKey]InstanceAdapterFactory{}
)

type instanceFactoryKey struct {
	typ      string
	apiStyle string
}

// RegisterInstanceAdapterFactory registers a factory that can construct a ProviderAdapter
// from an InstanceConfig. Provider packages should call this from init().
// typ and apiStyle must match what appears in providercfg.InstanceConfig.
func RegisterInstanceAdapterFactory(typ, apiStyle string, factory InstanceAdapterFactory) {
	if factory == nil {
		return
	}
	instanceFactoriesMu.Lock()
	instanceFactories[instanceFactoryKey{typ: typ, apiStyle: apiStyle}] = factory
	instanceFactoriesMu.Unlock()
}

// NewFromProviders constructs a Client from an explicit providercfg.Config.
// Each instance in cfg.Instances is mapped to its adapter by (Type, APIStyle)
// via factories registered with RegisterInstanceAdapterFactory. The configured
// Default instance is set as the client's default provider.
//
// Callers may pass the same EnvOption set accepted by NewFromEnv — in
// particular WithStateDir — to control the StateHome used for OAuth-backed
// adapters.
func NewFromProviders(cfg providercfg.Config, opts ...EnvOption) (*Client, error) {
	envCfg := EnvConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&envCfg)
		}
	}

	instanceFactoriesMu.Lock()
	factories := make(map[instanceFactoryKey]InstanceAdapterFactory, len(instanceFactories))
	for k, v := range instanceFactories {
		factories[k] = v
	}
	instanceFactoriesMu.Unlock()

	c := NewClient()

	for _, inst := range cfg.Instances {
		key := instanceFactoryKey{
			typ:      string(inst.Type),
			apiStyle: string(inst.APIStyle),
		}
		factory, ok := factories[key]
		if !ok {
			// Try the catch-all (type with empty apiStyle) as a fallback when
			// the exact style isn't registered separately.
			if inst.APIStyle != "" {
				factory, ok = factories[instanceFactoryKey{typ: string(inst.Type)}]
			}
		}
		if !ok {
			return nil, fmt.Errorf("provider %q: unknown type/apiStyle combination (%q, %q)", inst.Name, inst.Type, inst.APIStyle)
		}
		adapter, err := factory(inst, envCfg.StateHome)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", inst.Name, err)
		}
		c.Register(adapter)
	}

	if cfg.Default != "" {
		c.SetDefaultProvider(cfg.Default)
	}

	c.SetNameToTag(providercfg.NameToTag(cfg))

	return c, nil
}
