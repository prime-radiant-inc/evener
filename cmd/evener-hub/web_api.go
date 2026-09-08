package hub

import (
	"encoding/json"
	"errors"
	"net/http"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/hubapi"
)

func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeAPIJSON(w, status, hubapi.ErrorResponse{Error: msg})
}

func writeAPIWireError(w http.ResponseWriter, fallbackStatus int, err error) {
	wire, ok := wireErrorFromError(err)
	if !ok {
		writeAPIError(w, fallbackStatus, err.Error())
		return
	}
	writeAPIJSON(w, statusForWireError(wire, fallbackStatus), hubapi.ErrorResponse{
		Error:           wire.Message,
		Code:            wire.Code,
		EvenerErrorInfo: evenerErrorInfoFromData(wire.Data),
	})
}

func wireErrorFromError(err error) (appwire.WireError, bool) {
	if wire, ok := errors.AsType[appwire.WireError](err); ok {
		return wire, true
	}
	return appwire.WireError{}, false
}

func statusForWireError(wire appwire.WireError, fallback int) int {
	switch wire.Code {
	case appwire.CodeInvalidParams, appwire.CodeInvalidRequest:
		return http.StatusBadRequest
	case appwire.CodeMethodNotFound:
		return http.StatusNotFound
	case appwire.CodeConflict:
		return http.StatusConflict
	case appwire.CodeUnavailable:
		return http.StatusServiceUnavailable
	case appwire.CodeInternalError:
		return http.StatusInternalServerError
	default:
		return fallback
	}
}

func evenerErrorInfoFromData(data any) string {
	switch v := data.(type) {
	case appwire.ErrorData:
		return string(v.EvenerErrorInfo)
	case map[string]any:
		if info, ok := v["evenerErrorInfo"].(string); ok {
			return info
		}
	}
	return ""
}

func (s *WebServer) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	writeAPIJSON(w, http.StatusOK, hubapi.HealthResponse{
		Version:          buildinfo.Version(),
		MobileAPIVersion: hubapi.MobileAPIVersion,
		StartedAt:        s.startedAt,
		HubAddr:          s.cfg.HubAddr,
		RunDir:           s.cfg.RunDir,
		StateGlob:        s.apiStateGlob(),
		BackendGitSha:    buildinfo.GitSHA,
		FrontendHash:     s.frontendHash,
		Capabilities: hubapi.HealthCapabilities{
			TranscriptFollow: true,
			Fork:             true,
		},
	})
}

func (s *WebServer) handleAPIDebugSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusOK, s.appRPC.DebugSubscriptions())
}

func (s *WebServer) apiStateGlob() string {
	if s.cfg.Past == nil {
		return ""
	}
	return s.cfg.Past.StateGlob()
}

func warningPayload(raw json.RawMessage) map[string]any {
	params, message := appwire.DecodeWarningParams(raw)
	payload := map[string]any{"message": message}
	if params.Source != "" {
		payload["source"] = params.Source
	}
	if params.Title != "" {
		payload["title"] = params.Title
	}
	if params.Hint != "" {
		payload["hint"] = params.Hint
	}
	addDiagnosticDefaults(payload, message)
	return payload
}

func addDiagnosticDefaults(payload map[string]any, message string) {
	info := diagnostic.Classify(message)
	if _, ok := payload["source"]; !ok {
		payload["source"] = string(info.Source)
	}
	if _, ok := payload["title"]; !ok {
		payload["title"] = info.Title
	}
	if _, ok := payload["hint"]; !ok {
		payload["hint"] = info.Hint
	}
}

func warningMessage(raw json.RawMessage) string {
	_, message := appwire.DecodeWarningParams(raw)
	return message
}
