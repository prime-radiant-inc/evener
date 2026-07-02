package llm

import (
	"errors"
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
	c, _, err := newFromProviders(cfg, false, opts...)
	return c, err
}

// NewFromAvailableProviders constructs a Client from every provider instance
// that can initialize. Unknown type/apiStyle wiring still fails hard; adapter
// factory errors are returned as initialization errors while the healthy
// instances remain registered.
func NewFromAvailableProviders(cfg providercfg.Config, opts ...EnvOption) (*Client, []error, error) {
	return newFromProviders(cfg, true, opts...)
}

func newFromProviders(cfg providercfg.Config, allowPartial bool, opts ...EnvOption) (*Client, []error, error) {
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
	var initErrs []error

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
			return nil, nil, fmt.Errorf("provider %q: unknown type/apiStyle combination (%q, %q)", inst.Name, inst.Type, inst.APIStyle)
		}
		// Expand $ENV references in the api_key here — the one choke point
		// every instance adapter passes through — so a missing variable fails
		// just this instance, with its name in the error. Exception: the
		// openai (responses/auto) factory is OAuth-first and treats api_key
		// as a fallback, so an unresolvable reference clears the key and
		// lets the factory try stored OAuth; it fails with its own
		// no-credentials error when neither exists.
		apiKey, err := providercfg.ResolveAPIKey(inst.APIKey)
		if err != nil {
			if providercfg.BehaviorTag(string(inst.Type), string(inst.APIStyle)) == "openai" {
				apiKey = ""
			} else {
				wrapped := fmt.Errorf("provider %q: %w", inst.Name, err)
				if allowPartial {
					initErrs = append(initErrs, wrapped)
					continue
				}
				return nil, nil, wrapped
			}
		}
		inst.APIKey = apiKey
		// Resolve $ENV references in each header at the same choke point as
		// api_key, so a missing variable fails just this instance with its name
		// and the header key in the error.
		if len(inst.Headers) > 0 {
			resolved := make(map[string]string, len(inst.Headers))
			var hdrErr error
			for _, k := range sortedHeaderKeys(inst.Headers) {
				v, err := providercfg.ResolveHeaderValue(k, inst.Headers[k])
				if err != nil {
					hdrErr = fmt.Errorf("provider %q: %w", inst.Name, err)
					break
				}
				resolved[k] = v
			}
			if hdrErr != nil {
				if allowPartial {
					initErrs = append(initErrs, hdrErr)
					continue
				}
				return nil, nil, hdrErr
			}
			inst.Headers = resolved
		}
		adapter, err := factory(inst, envCfg.StateHome)
		if err != nil {
			wrapped := fmt.Errorf("provider %q: %w", inst.Name, err)
			if allowPartial {
				initErrs = append(initErrs, wrapped)
				continue
			}
			return nil, nil, wrapped
		}
		c.Register(adapter)
	}

	if allowPartial && len(c.ProviderNames()) == 0 && len(initErrs) > 0 {
		return nil, initErrs, fmt.Errorf("no providers initialized: %w", errors.Join(initErrs...))
	}
	if cfg.Default != "" {
		c.SetDefaultProvider(cfg.Default)
	}

	c.SetNameToTag(providercfg.NameToTag(cfg))

	return c, initErrs, nil
}
