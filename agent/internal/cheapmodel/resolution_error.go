package cheapmodel

// AttemptFailure identifies the selected route and its own error, independent
// of another route's error in a joined failure. Err can contain private provider
// text; consumers must extract safe metadata rather than serialize this value.
type AttemptFailure struct {
	Provider string
	Model    string
	Err      error
}

// ResolutionError retains the terminal and previous route failures without
// changing the aggregate cause used for retry and refusal classification.
type ResolutionError struct {
	Terminal AttemptFailure
	Previous []AttemptFailure
	cause    error
}

func (e *ResolutionError) Error() string { return e.cause.Error() }
func (e *ResolutionError) Unwrap() error { return e.cause }
