package appwire

// The personal AGENTS.md: a plain file at <config root>/AGENTS.md that every
// session loads ahead of the repo's own project docs
// (agent.LoadInstructionDocs). The hub reads and rewrites it whole; there is
// deliberately no revision or precondition (spec 2026-09-07 §1): the file is
// also hand-edited in editors, and the last write wins.
const (
	MethodEvenerSettingsAgentsDocGet     = "evener/settings/agentsDoc/get"
	MethodEvenerSettingsAgentsDocSet     = "evener/settings/agentsDoc/set"
	NotifyEvenerSettingsAgentsDocChanged = "evener/settings/agentsDoc/changed"
)

// AgentsDocResponse is the file as the hub sees it: the get result, the set
// result, and the changed broadcast all carry this shape. A missing file is
// Exists=false with empty Content, never an error.
type AgentsDocResponse struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Content string `json:"content"`
}

// AgentsDocSetParams replaces the whole file with Content, byte for byte.
type AgentsDocSetParams struct {
	Content string `json:"content"`
}
