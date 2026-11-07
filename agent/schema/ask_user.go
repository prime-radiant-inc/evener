package schema

// AskUserState is the canonical public content of a successfully executed ask_user call.
// Only the validated executor writes it, never a reconstruction from sent arguments.
type AskUserState struct {
	Version   int               `json:"version"`
	Questions []AskUserQuestion `json:"questions"`
}

// AskUserOption is a validated public option, including its full detail.
type AskUserOption struct {
	Label       string `json:"label"`
	Detail      string `json:"detail,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// AskUserQuestion is one question from a pending ask_user call, with full
// option detail and the call's optional fields: whether several options may
// be chosen, why the answer matters, and the fallback the model would take.
type AskUserQuestion struct {
	Header       string          `json:"header,omitempty"`
	Question     string          `json:"question"`
	MultiSelect  bool            `json:"multi_select,omitempty"`
	Why          string          `json:"why,omitempty"`
	IfUnanswered string          `json:"if_unanswered,omitempty"`
	Options      []AskUserOption `json:"options,omitempty"`
}
