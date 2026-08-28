package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/hubapi"
)

var ensureAPIActionAvailable = func(s *WebServer, id, action string) error {
	return s.ensureSessionActionAvailable(id, action)
}

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
			RemoteSources:    len(s.cfg.CodexSources) > 0,
		},
	})
}

func (s *WebServer) apiStateGlob() string {
	if s.cfg.Past == nil {
		return ""
	}
	return s.cfg.Past.StateGlob()
}

func warningPayload(raw json.RawMessage) map[string]any {
	message := warningMessage(raw)
	payload := map[string]any{"message": message}
	var params struct {
		Source string `json:"source"`
		Title  string `json:"title"`
		Hint   string `json:"hint"`
	}
	if json.Unmarshal(raw, &params) == nil {
		if params.Source != "" {
			payload["source"] = params.Source
		}
		if params.Title != "" {
			payload["title"] = params.Title
		}
		if params.Hint != "" {
			payload["hint"] = params.Hint
		}
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
	var params struct {
		Message string `json:"message"`
		Warning any    `json:"warning"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return string(raw)
	}
	if strings.TrimSpace(params.Message) != "" {
		return params.Message
	}
	switch warning := params.Warning.(type) {
	case string:
		return warning
	case map[string]any:
		if message, ok := warning["message"].(string); ok {
			return message
		}
	}
	return string(raw)
}

func (s *WebServer) handleAPIClear(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !s.isLive(id) {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	if err := ensureAPIActionAvailable(s, id, "clear"); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	refText := appRefFromRouteID(id)
	unlockDeletionTarget := lockDeletionTarget(s.cfg, refText, "")
	defer unlockDeletionTarget()
	if err := deletionFenceError(s.cfg, refText, "", ""); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	source, err := sourceForThread(s.sources, refText, "")
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	resp, err := source.ClearThread(r.Context(), appwire.ThreadClearParams{Ref: refText})
	if err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	outRefText := resp.Ref
	if outRefText == "" {
		outRefText = resp.Thread.Evener.Ref
	}
	ref, err := hubapi.ParseRef(outRefText)
	if err != nil {
		ref = hubapi.LocalRef(resp.Thread.ID)
	}
	writeAPIJSON(w, http.StatusOK, hubapi.RefResponse{
		Ref:       ref.String(),
		HostID:    ref.HostID,
		SessionID: ref.SessionID,
	})
}

func (s *WebServer) handleAPIModel(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !s.isLive(id) {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	ref := appRefFromRouteID(id)
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, model := splitProviderModel(body.Model)
	if model == "" {
		model = body.Model
	}
	if err := ensureAPIActionAvailable(s, id, "model"); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	unlockDeletionTarget := lockDeletionTarget(s.cfg, ref, "")
	defer unlockDeletionTarget()
	if err := deletionFenceError(s.cfg, ref, "", ""); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	if err := source.SetThreadModel(r.Context(), appwire.ThreadModelSetParams{Ref: ref, ModelProvider: provider, Model: model}); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAPIReasoningEffort changes the reasoning effort of a running session.
func (s *WebServer) handleAPIReasoningEffort(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if !s.isLive(id) {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	ref := appRefFromRouteID(id)
	var body struct {
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	unlockDeletionTarget := lockDeletionTarget(s.cfg, ref, "")
	defer unlockDeletionTarget()
	if err := deletionFenceError(s.cfg, ref, "", ""); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	source, err := sourceForThread(s.sources, ref, "")
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "session not live")
		return
	}
	if err := source.SetThreadReasoningEffort(r.Context(), appwire.ThreadReasoningEffortSetParams{Ref: ref, ReasoningEffort: strings.TrimSpace(body.ReasoningEffort)}); err != nil {
		writeAPIWireError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
