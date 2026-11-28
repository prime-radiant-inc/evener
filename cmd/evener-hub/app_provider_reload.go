package hub

import (
	"crypto/sha256"
	"fmt"
	"os"
	"slices"
)

// userLayerEnabled reports whether this hub has an editable providers.toml to
// read. A registry can be wired without one (EVENER_PROVIDERS_CONFIG present
// and empty), which is the shape a child gets; the record watcher still has to
// run there, so only the file read below is gated on this.
func (c *hubInstancesController) userLayerEnabled() bool {
	return c.providersConfigPath != "" && (c.auth == nil || !c.auth.noUserLayer)
}

// refreshProviderFile adopts external edits under the same locks as instance
// mutations. Failed loads retain a recovery owner: the notice watcher retries
// once per tick even when the edited bytes have not changed. Only a changed
// registry or diagnostic invalidates clients, so an unchanged error is quiet.
func (c *hubInstancesController) refreshProviderFile() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.auth != nil {
		c.auth.credMu.Lock()
		defer c.auth.credMu.Unlock()
	}
	// The provider file exists only with a user layer. The Copilot record
	// observation and the registry reload below run either way: a hub started
	// with EVENER_PROVIDERS_CONFIG empty still serves the account's roster and
	// must adopt an external login or logout.
	fileChanged := false
	if c.userLayerEnabled() {
		raw, err := os.ReadFile(c.providersConfigPath)
		signature := providerConfigSignature(raw)
		if err != nil {
			signature = err.Error()
		}
		fileChanged = c.providerFileSignature != signature
		c.providerFileSignature = signature
	}
	var copilot map[string]string
	reg := c.reg.Get()
	if reg != nil {
		copilot = reg.CopilotAuthSignature()
	}
	changedAccounts := changedCopilotAccounts(c.copilotAuthSignature, copilot)
	c.copilotAuthSignature = copilot
	if !fileChanged && len(changedAccounts) == 0 && !c.reg.WritesRefused() {
		return false
	}
	generation := c.reg.Generation()
	diagnostics := c.reg.Diagnostics()
	if reg != nil {
		for _, name := range changedAccounts {
			reg.ForgetLive(name)
		}
	}
	_ = c.reg.Reload()
	return generation != c.reg.Generation() || !slices.Equal(diagnostics, c.reg.Diagnostics())
}

// changedCopilotAccounts names the instances whose mint-free record state
// differs between two observations, including a record that appeared or went
// away.
func changedCopilotAccounts(before, after map[string]string) []string {
	var names []string
	for name, state := range after {
		if old, ok := before[name]; !ok || old != state {
			names = append(names, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			names = append(names, name)
		}
	}
	return names
}

func providerConfigSignature(raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
