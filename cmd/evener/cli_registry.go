package main

import (
	"io"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// cliRegistryOptions are extra registry options every read-only CLI load
// appends; tests set it to inject a catalog fixture or a controlled
// environment.
var cliRegistryOptions []registry.Option

// loadCLIRegistry loads the registry and the credentials store the way
// `evener models` and `evener providers` do: offline, because `evener models
// refresh` is the one explicit path to the network (spec §6.4, §11.1), plus
// the caller's own options. An old-schema providers.toml comes back as
// registry.ErrOldSchema and the command exits with it (spec §14.1).
func loadCLIRegistry(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
	opts := make([]registry.Option, 0, len(cliRegistryOptions)+len(extra)+1)
	opts = append(opts, registry.WithOffline(true))
	opts = append(opts, cliRegistryOptions...)
	opts = append(opts, extra...)
	return cmdutil.LoadRegistry(opts...)
}

// loadRegistryForCLI is loadCLIRegistry plus the notices a command announces
// once: the registry's load warnings and its stray OAuth records (spec §9.5,
// §14.1).
func loadRegistryForCLI(stderr io.Writer) (*registry.Registry, *credentials.Store, error) {
	r, store, err := loadCLIRegistry()
	if err != nil {
		return nil, nil, err
	}
	printRegistryNotices(stderr, r)
	return r, store, nil
}
