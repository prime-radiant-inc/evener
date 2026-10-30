package hubendpoint

// ValidationError identifies a rejected endpoint field without retaining the
// supplied endpoint, which may contain credentials or other secrets.
type ValidationError struct {
	Code  string
	Field string
}

func (e *ValidationError) Error() string {
	return "invalid " + e.Field + " (" + e.Code + ")"
}

func invalid(code, field string) error {
	return &ValidationError{Code: code, Field: field}
}
