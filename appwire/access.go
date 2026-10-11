package appwire

import "strings"

// SandboxOff is the sandbox mode of an unsandboxed session.
const SandboxOff = "off"

// SessionAccess is the Access a session reports for the sandbox request its
// configuration persists: the mode name (empty means off) and the network
// decision (nil means the default, on). A session that could not enforce its
// mode never starts, so the persisted request is what the session enforces.
func SessionAccess(sandbox string, network *bool) *ThreadAccess {
	mode := strings.TrimSpace(sandbox)
	if mode == "" || mode == SandboxOff {
		return &ThreadAccess{Sandbox: SandboxOff, Network: true}
	}
	return &ThreadAccess{Sandbox: mode, Network: network == nil || *network}
}
