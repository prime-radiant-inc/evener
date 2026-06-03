package schema

// WorkspaceInfo captures the directory structure and key files in the
// working directory. Injected into the system prompt so the model starts
// with awareness of what's available — no discovery round needed.
type WorkspaceInfo struct {
	Tree      string `json:"tree,omitempty"`       // indented directory listing
	BuildInfo string `json:"build_info,omitempty"` // build system summary
}
