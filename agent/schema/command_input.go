package schema

// CommandInputRecord preserves explicit command intent independently of the
// expanded message and of any skill with the same canonical spelling.
type CommandInputRecord struct {
	OriginalText string   `json:"original_text"`
	Names        []string `json:"names"`
}
