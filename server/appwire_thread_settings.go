package server

import (
	"context"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

func (s *Server) handleAppThreadCompactStart(ctx context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.compactFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("compact not available")
	}
	return appwire.EmptyResponse{}, fn(ctx)
}

func (s *Server) handleAppThreadShutdown(ctx context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.shutdownFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("shutdown not available")
	}
	// The shutdown ends the process, which closes this socket, so the reply
	// this handler owes has to reach the transport first: a reply still queued
	// in the connection's send loop when the process exits is never written,
	// and the client reads EOF in its place (#1501). Only a request with no
	// transport to wait on starts the shutdown right away.
	appserver.RunAfterResponseWritten(ctx, func() { go fn() })
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadModelSet(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	model := strings.TrimSpace(params.Model)
	if model == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("model is required")
	}
	if provider := strings.TrimSpace(params.ModelProvider); provider != "" {
		model = provider + "/" + model
	}
	s.mu.RLock()
	processing := s.processing
	fn := s.modelFunc
	s.mu.RUnlock()
	if processing {
		return appwire.EmptyResponse{}, appwire.Conflict("session is processing")
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("model change not available")
	}
	if err := fn(model); err != nil {
		// Every SetModel failure is mapped to InvalidParams deliberately: the
		// hook's error modes today are all validation-shaped (unknown model,
		// not a member of the instance's catalog, unresolvable ref). Revisit
		// this blanket mapping if SetModel grows I/O failure modes (e.g. a live
		// ListModels fetch failing), which would deserve a distinct code.
		return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadVisionModelSet(_ context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	processing := s.processing
	fn := s.visionModelFunc
	s.mu.RUnlock()
	if processing {
		return appwire.EmptyResponse{}, appwire.Conflict("session is processing")
	}
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("vision model change not available")
	}
	// "" and "off" are legitimate setting values (session-model and disabled),
	// so unlike model/set there is no empty-value rejection here; ref shape is
	// the session's job to validate (Session.SetVisionModel).
	if err := fn(params.VisionModel); err != nil {
		return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}

func (s *Server) handleAppThreadNameSet(_ context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("name is required")
	}
	s.mu.RLock()
	fn := s.nameFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("rename not available")
	}
	return appwire.EmptyResponse{}, fn(name)
}

func (s *Server) handleAppThreadReasoningEffortSet(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.EmptyResponse{}, err
	}
	s.mu.RLock()
	fn := s.reasoningEffortFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("reasoning effort change not available")
	}
	// Normalize disable-aliases to "none" and reject unknown vocabulary, so a
	// typo or direct API call can't persist a provider-rejected effort that
	// breaks later requests.
	effort := llm.NormalizeReasoningEffort(params.ReasoningEffort)
	if err := llm.ValidateReasoningEffort(effort); err != nil {
		return appwire.EmptyResponse{}, appwire.InvalidParams("invalid reasoning effort: " + params.ReasoningEffort)
	}
	return appwire.EmptyResponse{}, fn(effort)
}

func (s *Server) requireRootMutationTarget(rawRef, threadID string) error {
	target := strings.TrimSpace(threadID)
	if strings.TrimSpace(rawRef) != "" {
		ref, err := appwire.ParseRef(rawRef)
		if err != nil {
			return appwire.InvalidParams(err.Error())
		}
		if ref.SourceID != "local" {
			return appwire.SessionUnavailable("thread is unavailable")
		}
		s.mu.RLock()
		root := s.appThreadID
		stableRef := s.appRef
		s.mu.RUnlock()
		if stableRef != "" && strings.TrimSpace(rawRef) == stableRef {
			target = root
		} else {
			target = ref.ThreadID
		}
	}
	root := s.appProjectionThreadID()
	if target != "" && target != root {
		return appwire.SessionUnavailable("thread is not served by this daemon: " + target)
	}
	return nil
}

// requireRootMutationIdentity checks both halves of a retry-safe mutation's
// target. The ref identifies the stable workspace; ExpectedInstanceID fences
// the live session generation behind it. A clear intentionally keeps the ref
// while replacing that instance, so accepting only the ref would let a delayed
// old-generation outbox entry run against the replacement.
func (s *Server) requireRootMutationIdentity(rawRef, threadID, expectedInstanceID, clientMutationID string) error {
	if clientMutationID == "" {
		return appwire.InvalidParams("clientMutationId is required")
	}
	if expectedInstanceID == "" {
		return appwire.InvalidParams("expectedInstanceId is required")
	}
	if err := s.requireRootMutationTarget(rawRef, threadID); err != nil {
		return err
	}
	s.mu.RLock()
	currentInstanceID := s.appThreadID
	s.mu.RUnlock()
	if expectedInstanceID != currentInstanceID {
		return appwire.MutationNotAccepted(clientMutationID, "thread instance is stale")
	}
	return nil
}
