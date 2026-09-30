package hub

import (
	"crypto/sha256"
	"fmt"
	"os"
	"slices"
)

// refreshProviderFile adopts external edits under the same locks as instance
// mutations. Failed loads retain a recovery owner: the notice watcher retries
// once per tick even when the edited bytes have not changed. Only a changed
// registry or diagnostic invalidates clients, so an unchanged error is quiet.
func (c *hubInstancesController) refreshProviderFile() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.auth != nil {
		if c.auth.noUserLayer {
			return false
		}
		c.auth.credMu.Lock()
		defer c.auth.credMu.Unlock()
	}
	raw, err := os.ReadFile(c.providersConfigPath)
	signature := fmt.Sprintf("%x", sha256.Sum256(raw))
	if err != nil {
		signature = err.Error()
	}
	if c.providerFileSignature == signature && !c.reg.WritesRefused() {
		return false
	}
	c.providerFileSignature = signature
	generation := c.reg.Generation()
	diagnostics := c.reg.Diagnostics()
	_ = c.reg.Reload()
	return generation != c.reg.Generation() || !slices.Equal(diagnostics, c.reg.Diagnostics())
}
