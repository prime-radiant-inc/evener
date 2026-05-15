package agent

import (
	"strings"

	"primeradiant.com/serf/internal/diagnostic"
)

func errorDataFromError(err error) ErrorData {
	message := ""
	if err != nil {
		message = err.Error()
	}
	info := diagnostic.FromError(err)
	return ErrorData{
		Error:  message,
		Source: string(info.Source),
		Title:  info.Title,
		Hint:   info.Hint,
	}
}

func warningDataFromError(message string, err error) WarningData {
	info := diagnostic.FromError(err)
	return WarningData{
		Message: strings.TrimSpace(message),
		Source:  string(info.Source),
		Title:   info.Title,
		Hint:    info.Hint,
	}
}

func enrichDiagnosticData(kind EventKind, data any) any {
	switch kind {
	case EventWarning:
		switch d := data.(type) {
		case WarningData:
			return enrichWarningData(d)
		case *WarningData:
			if d == nil {
				return data
			}
			enriched := enrichWarningData(*d)
			return &enriched
		}
	case EventError:
		switch d := data.(type) {
		case ErrorData:
			return enrichErrorData(d)
		case *ErrorData:
			if d == nil {
				return data
			}
			enriched := enrichErrorData(*d)
			return &enriched
		}
	}
	return data
}

func enrichWarningData(data WarningData) WarningData {
	info := diagnostic.FromFields(data.Source, data.Title, data.Hint, data.Message)
	data.Source = string(info.Source)
	data.Title = info.Title
	data.Hint = info.Hint
	return data
}

func enrichErrorData(data ErrorData) ErrorData {
	info := diagnostic.FromFields(data.Source, data.Title, data.Hint, data.Error)
	data.Source = string(info.Source)
	data.Title = info.Title
	data.Hint = info.Hint
	return data
}

func setAPICallDiagnostic(call *TranscriptAPICall, err error) {
	if call == nil {
		return
	}
	info := diagnostic.FromError(err)
	call.Source = string(info.Source)
	call.Title = info.Title
	call.Hint = info.Hint
}
