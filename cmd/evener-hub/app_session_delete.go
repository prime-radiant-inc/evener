package hub

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

type sessionDeleteHandler func(context.Context, appwire.SessionDeleteParams) (appwire.SessionDeleteResponse, error)

func registerSessionDeleteHandler(server *appserver.Server, handler sessionDeleteHandler) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionDelete, func(ctx context.Context, params appwire.SessionDeleteParams) (appwire.SessionDeleteResponse, error) {
		if handler == nil {
			return appwire.SessionDeleteResponse{}, appwire.Unavailable("session deletion unavailable")
		}
		return handler(ctx, params)
	})
}

// sessionDelete removes one ended or confirmed-crashed local session without
// touching project siblings. It reuses the project-deletion ownership and
// cleanup machinery so both destructive paths share the same liveness,
// reservation, artifact, and decision contracts.
func (s *WebServer) sessionDelete(ctx context.Context, params appwire.SessionDeleteParams) (appwire.SessionDeleteResponse, error) {
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appwire.SessionDeleteResponse{}, appwire.InvalidParams(err.Error())
	}
	if ref.SourceID != "local" {
		return appwire.SessionDeleteResponse{}, appwire.InvalidParams("only local sessions can be deleted")
	}
	threadID := ref.ThreadID
	if err := identifier.ValidateSessionID(threadID); err != nil {
		return appwire.SessionDeleteResponse{}, appwire.InvalidParams("invalid session ID: " + err.Error())
	}
	if s.cfg.Past == nil {
		return appwire.SessionDeleteResponse{}, appwire.InternalError("past index not configured")
	}
	pe, ok := s.cfg.Past.Find(threadID)
	if !ok {
		if decisionErrors := s.scrubSessionDecisions(threadID); len(decisionErrors) > 0 {
			return appwire.SessionDeleteResponse{}, appwire.InternalError(strings.Join(decisionErrors, "; "))
		}
		return s.sessionDeleteResponse(ctx, nil, nil, "")
	}

	target := hubcore.DeletionTarget{Ref: localAppRef(threadID), ThreadID: threadID}
	record := hubcore.DeletionRecord{Targets: []hubcore.DeletionTarget{target}}
	stateDirs := map[string]string{threadID: pe.StateDir}
	release, ownerErr := s.acquireProjectDeletionOwnership(record, stateDirs)
	if ownerErr != nil {
		var skipped []projectDeleteSkip
		if errors.Is(ownerErr.Err, llm.ErrAPILogTargetLocked) || ownerErr.Live {
			skipped = appendProjectDeleteLiveSkip(nil, threadID)
		} else {
			skipped = []projectDeleteSkip{{ID: threadID, Reason: ownerErr.Error()}}
		}
		return s.sessionDeleteResponse(ctx, nil, skipped, projectKeyForStateDir(pe.StateDir))
	}
	defer func() {
		if release != nil {
			release()
		}
	}()

	deleted, skip, decisionErrors := s.cleanupProjectDeletionTargetAndDecisions(pe.StateDir, threadID)
	if !deleted {
		return s.sessionDeleteResponse(ctx, nil, []projectDeleteSkip{*skip}, projectKeyForStateDir(pe.StateDir))
	}

	rebuilt, err := rebuildProjectDeletionPast(s.cfg.Past)
	if err != nil {
		decisionErrors = append(decisionErrors, "past index rebuild error: "+err.Error())
	}
	if s.cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, s.cfg.Roster); err != nil {
			decisionErrors = append(decisionErrors, "roster refresh error: "+err.Error())
		}
	}
	if s.cfg.Inputs != nil {
		s.cfg.Inputs.Bump()
	}
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	if !rebuilt {
		s.navigation.Invalidate(navigationChangeHint{})
	}
	if len(decisionErrors) > 0 {
		return appwire.SessionDeleteResponse{}, appwire.InternalError(strings.Join(decisionErrors, "; "))
	}
	release()
	release = nil
	return s.sessionDeleteResponse(ctx, []string{threadID}, nil, projectKeyForStateDir(pe.StateDir))
}

func (s *WebServer) sessionDeleteResponse(
	ctx context.Context,
	deleted []string,
	skipped []projectDeleteSkip,
	project string,
) (appwire.SessionDeleteResponse, error) {
	wireSkipped := make([]appwire.DeletionSkip, len(skipped))
	for i, skip := range skipped {
		wireSkipped[i] = appwire.DeletionSkip(skip)
	}
	response := appwire.SessionDeleteResponse{
		Deleted:    append([]string{}, deleted...),
		Skipped:    wireSkipped,
		Navigation: s.emptyNavigationMutation(),
	}
	if len(deleted) == 0 {
		return response, nil
	}
	hint := navigationChangeHint{AllLoadedProjects: project == ""}
	if project != "" {
		hint.Projects = []string{project}
	}
	if s.navigation == nil {
		return appwire.SessionDeleteResponse{}, appwire.Unavailable("navigation unavailable")
	}
	navigation, err := s.navigation.Refresh(ctx, hint)
	if err != nil {
		return appwire.SessionDeleteResponse{}, appwire.Unavailable(err.Error())
	}
	response.Navigation = navigation
	return response, nil
}
