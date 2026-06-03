package agent

import (
	"errors"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/diagnostic"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
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

func warningDataFromError(message string, err error) events.WarningData {
	info := diagnostic.FromError(err)
	return events.WarningData{
		Message: strings.TrimSpace(message),
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

func setAPICallDiagnostic(call *transcript.APICall, err error) {
	if call == nil {
		return
	}
	info := diagnostic.FromError(err)
	call.Source = string(info.Source)
	call.Title = info.Title
	call.Hint = info.Hint
}
