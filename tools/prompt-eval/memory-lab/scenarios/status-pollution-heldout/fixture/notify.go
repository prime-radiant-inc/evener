package notify

import "errors"

// Message is one outgoing notification.
type Message struct {
	To   string
	Body string
}

// Validate reports whether m can be sent.
func Validate(m Message) error {
	if m.To == "" {
		return errors.New("missing recipient")
	}
	if m.Body == "" {
		return errors.New("empty body")
	}
	return nil
}
