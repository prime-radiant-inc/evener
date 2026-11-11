package main

import (
	"path/filepath"

	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/llm"
)

// hostQuotaCoordinator returns the rate-limit coordinator shared by every
// session this host process starts, keyed per provider instance under the
// host state root. An unresolvable state root disables coordination: each
// session keeps its own wall budget, the pre-coordinator behavior.
func hostQuotaCoordinator() *llm.QuotaCoordinator {
	root, err := userdirs.DefaultStateRoot()
	if err != nil {
		return nil
	}
	return llm.NewQuotaCoordinator(filepath.Join(root, "quota"))
}
