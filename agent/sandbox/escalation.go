package sandbox

// EscalationKind discriminates the two approval-card shapes M7 can raise from a
// DeniedError. The distinction is honesty about side effects: a file-tool denial
// refused a single precise path with NO partial side effect (the in-process
// securepath layer denies before it touches anything), so re-running it is safe
// and total. A shell/kernel denial is heuristic (the denied path is attributed
// best-effort) and the command may have PARTIALLY executed before it was stopped,
// so the card must warn that approving re-runs the whole command start-to-finish.
type EscalationKind string

const (
	// EscalationFileTool is a precise, side-effect-free file-tool denial.
	EscalationFileTool EscalationKind = "file_tool"
	// EscalationShell is a heuristic shell/kernel denial that may have partially run.
	EscalationShell EscalationKind = "shell"
)

// EscalationRequest is the wire-agnostic value a session raises for one sandbox
// denial and the projector turns into a human approval card. It is deliberately
// leaf-level (no AppWire import) so both agent/ and internal/appprojector/ can
// share it. It carries only what the human needs to decide — never file contents,
// and never a sensitive path even by basename (DeniedPath is already redacted).
//
// It is NEVER written to the model's transcript: the escalation is invisible to
// the model, which cannot trigger, approve, observe, or replay it.
type EscalationRequest struct {
	// ID is the opaque handle the resolve request targets. Generated per denial.
	ID string
	// Mode is the active sandbox mode, for the card's legible message.
	Mode Mode
	// Tool is the denied tool (e.g. "write_file").
	Tool string
	// Kind selects the card shape (file_tool vs shell).
	Kind EscalationKind
	// DeniedPath is the path shown on the human's approval card. For an ordinary
	// (non-sensitive) denial — the only kind that escalates — it is the FULL literal
	// path, so the human gives INFORMED consent to exactly what will be accessed
	// (a basename alone could hide a symlinked parent). A sensitive path never
	// escalates, but as a defensive floor it renders as "<denied>" here.
	DeniedPath string
	// Command is the full shell command (shell kind only; empty for file tools).
	Command string
	// OutputSoFar is what a partially-run command emitted before it was stopped
	// (shell kind only). Bounded by the caller before it rides the card.
	OutputSoFar string
	// PartiallyRan is true when the command may have executed before the denial
	// (shell kind only), driving the "already partially ran" caveat on the card.
	PartiallyRan bool
}

// EscalationDecision is the human's answer to an EscalationRequest, delivered
// out-of-band from the model loop by the UI's resolve request.
type EscalationDecision struct {
	// Approve re-runs the single denied invocation with the one path granted;
	// false returns the typed denial to the model, exactly as a non-interactive
	// session already does.
	Approve bool
}

// NewEscalationRequest builds an EscalationRequest from a typed denial and an
// opaque id. A denial that carries a Command is the shell kind (and may have
// partially run); otherwise it is a file-tool denial. DeniedPath is the FULL path
// for informed consent (a sensitive denial — which never escalates — degrades to
// "<denied>" defensively).
func NewEscalationRequest(id string, d *DeniedError) EscalationRequest {
	kind := EscalationFileTool
	partial := false
	if d.Command != "" {
		kind = EscalationShell
		partial = true
	}
	deniedPath := d.Path
	if d.Sensitive {
		deniedPath = "<denied>"
	}
	return EscalationRequest{
		ID:           id,
		Mode:         d.Mode,
		Tool:         d.Tool,
		Kind:         kind,
		DeniedPath:   deniedPath,
		Command:      d.Command,
		OutputSoFar:  d.OutputSoFar,
		PartiallyRan: partial,
	}
}
