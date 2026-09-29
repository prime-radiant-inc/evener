package agent

import (
	"errors"
	"strings"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/llm"
)

func errorDataFromError(err error) events.ErrorData {
	message := ""
	if err != nil {
		message = err.Error()
	}
	info := diagnostic.FromError(err)
	return events.ErrorData{
		Error:  message,
		Source: string(info.Source),
		Title:  info.Title,
		Hint:   info.Hint,
	}
}

// warningCauseLimit bounds the error text a warning's message carries.
const warningCauseLimit = 512

// warningDataFromError is the warning for a failure: its message is the label
// and the error's text ("<label>: <err>"), flattened to one line and bounded,
// so a repeating warning says why on the phone, the web and in hooks (#3386).
// The error also classifies the Title and Hint. The label names what failed
// and ends without a colon: the helper adds ": " before the cause.
func warningDataFromError(label string, err error) events.WarningData {
	data := bareWarningDataFromError(label, err)
	if err == nil {
		return data
	}
	// errors.Join separates its errors with newlines; one line reads "; ". A
	// lone carriage return, which would let the cause overwrite the label on
	// a terminal, reads as a space; other whitespace controls (tab, NEL) read
	// as a space and every other control (an ANSI sequence's ESC, backspace,
	// DEL) is dropped, as text re-served to a terminal is (stripTextControls).
	var lines []string
	for line := range strings.SplitSeq(err.Error(), "\n") {
		if line = strings.TrimSpace(stripTextControls(strings.ReplaceAll(line, "\r", " "))); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > 0 {
		data.Message += ": " + runetrim.Cut(strings.Join(lines, "; "), warningCauseLimit)
	}
	return data
}

// bareWarningDataFromError is warningDataFromError without the error's text
// in the message, for an error from a model call: a provider's error body can
// echo the user's own request, which would otherwise reach Notification
// hooks. Its callers are retries that recover on their own, so the text adds
// nothing; the error still classifies the Title and Hint.
func bareWarningDataFromError(label string, err error) events.WarningData {
	info := diagnostic.FromError(err)
	return events.WarningData{
		Message: strings.TrimSpace(label),
		Source:  string(info.Source),
		Title:   info.Title,
		Hint:    info.Hint,
	}
}

func enrichDiagnosticData(kind events.EventKind, data events.EventData) events.EventData {
	switch kind {
	case events.EventWarning:
		switch d := data.(type) {
		case events.WarningData:
			return enrichWarningData(d)
		case *events.WarningData:
			if d == nil {
				return data
			}
			enriched := enrichWarningData(*d)
			return &enriched
		}
	case events.EventError:
		switch d := data.(type) {
		case events.ErrorData:
			return enrichErrorData(d)
		case *events.ErrorData:
			if d == nil {
				return data
			}
			enriched := enrichErrorData(*d)
			return &enriched
		}
	}
	return data
}

func enrichWarningData(data events.WarningData) events.WarningData {
	info := diagnostic.FromFields(data.Source, data.Title, data.Hint, data.Message)
	data.Source = string(info.Source)
	data.Title = info.Title
	data.Hint = info.Hint
	return data
}

func enrichErrorData(data events.ErrorData) events.ErrorData {
	info := diagnostic.FromFields(data.Source, data.Title, data.Hint, data.Error)
	data.Source = string(info.Source)
	data.Title = info.Title
	data.Hint = info.Hint
	return data
}

// providerCauseFromError returns a structured ErrorCause for an err that
// unwraps to an llm.Error with a non-empty Provider. Returns nil otherwise
// — consumers treat a nil Cause as "source unknown" (kata ts0x).
func providerCauseFromError(err error, model string) *events.ErrorCause {
	if err == nil {
		return nil
	}
	// A ConfigurationError is a local configuration problem, not an HTTP
	// provider failure, so it never yields a "provider" cause. The one
	// attributed form is a sign-in failure: name the instance the user must
	// sign in to, so a failed turn can say "<instance> sign-in expired"
	// instead of recording no cause (#2705).
	if ce, ok := errors.AsType[*llm.ConfigurationError](err); ok {
		if provider := ce.Provider(); errors.Is(ce, llm.ErrSignInRequired) && provider != "" {
			return &events.ErrorCause{Kind: signInRequiredCause, Provider: provider}
		}
		return nil
	}
	var le llm.Error
	if !errors.As(err, &le) {
		return nil
	}
	provider := strings.TrimSpace(le.Provider())
	if provider == "" {
		return nil
	}
	return &events.ErrorCause{
		Kind:     "provider",
		Provider: provider,
		Model:    model,
		Status:   le.StatusCode(),
	}
}

// signInRequiredCause is the Cause.Kind for a sign-in failure.
const signInRequiredCause = "signInRequired"
