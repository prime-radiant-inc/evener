package hub

import (
	"context"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
)

type sessionDeleteHandler func(context.Context, appwire.SessionDeleteParams) (appwire.SessionDeleteResponse, error)

func registerSessionDeleteHandler(server *appserver.Server, handler sessionDeleteHandler) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionDelete, func(ctx context.Context, params appwire.SessionDeleteParams) (appwire.SessionDeleteResponse, error) {
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
	if ref.SourceID != appwire.LocalSourceID {
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

	target := hubcore.DeletionTarget{Ref: localAppRef(threadID), ThreadID: threadID}
	projectID := projectKeyForStateDir(pe.StateDir)
	record := hubcore.DeletionRecord{Targets: []hubcore.DeletionTarget{target}}
	stateDirs := map[string]string{threadID: pe.StateDir}
	release, ownerErr := s.acquireProjectDeletionOwnership(ctx, record, stateDirs)
	if ownerErr != nil {
		// A canceled or expired request must fail, as the fresh project-delete
		// path does, rather than answer success-with-skip for a request that was
		// abandoned before it could decide.
		if err := ctx.Err(); err != nil {
			return appwire.SessionDeleteResponse{}, err
		}
		return s.sessionDeleteResponse(ctx, nil, []projectDeleteSkip{ownershipSkip(threadID, ownerErr)}, projectID)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	// Ownership is held but the request may have been abandoned while it waited;
	// fail before the destructive cleanup, as the fresh project-delete path does.
	if err := ctx.Err(); err != nil {
		return appwire.SessionDeleteResponse{}, err
	}
	if !ok {
		// The shared owner confirmed absence. A prior deletion's tombstone
		// supplies the real bucket for retrying any unfinished artifact cleanup.
		if stateDir := stateDirs[threadID]; stateDir != "" {
			if err := s.cleanupProjectDeletionTarget(stateDir, threadID); err != nil {
				return s.sessionDeleteResponse(ctx, nil, []projectDeleteSkip{{ID: threadID, Reason: err.Error()}}, "")
			}
		}
		if decisionErrors := s.scrubSessionDecisions(threadID); len(decisionErrors) > 0 {
			return appwire.SessionDeleteResponse{}, appwire.InternalError(strings.Join(decisionErrors, "; "))
		}
		return s.sessionDeleteResponse(ctx, nil, nil, "")
	}

	if s.cfg.DeletionStore == nil {
		return appwire.SessionDeleteResponse{}, appwire.InternalError("deletion store is not configured")
	}
	if s.deletionStoreErr != nil {
		return appwire.SessionDeleteResponse{}, appwire.InternalError("load deletion state: " + s.deletionStoreErr.Error())
	}
	// Fence the target durably before the destructive cleanup, exactly as the
	// project-delete path does. A later read of this ref must settle as a
	// terminal deletion instead of retrying an unavailable source; a failed
	// cleanup leaves the fence in progress for the retry to finish.
	fence, err := s.cfg.DeletionStore.BeginSession(projectID, target)
	if err != nil {
		return appwire.SessionDeleteResponse{}, appwire.InternalError("commit deletion fence: " + err.Error())
	}

	deleted, skip, decisionErrors := s.cleanupProjectDeletionTargetAndDecisions(pe.StateDir, threadID)
	if !deleted {
		// The sweep may refuse after the metadata is already gone (a later
		// rendezvous/log removal failed). Only a session whose metadata still
		// exists is intact and resumable; that one must not stay fenced as a
		// terminal deletion. Once the metadata is gone the deletion is
		// effectively done, so its fence stays authoritative.
		if sessionMetaFilePresent(pe.StateDir, threadID) {
			if abandonErr := s.cfg.DeletionStore.Abandon(fence.ProjectID, fence.Generation); abandonErr != nil {
				skip.Reason += "; abandon deletion fence: " + abandonErr.Error()
			}
		}
		return s.sessionDeleteResponse(ctx, nil, []projectDeleteSkip{*skip}, projectID)
	}

	rebuilt, err := rebuildProjectDeletionPast(s.cfg.Past)
	if err != nil {
		decisionErrors = append(decisionErrors, "past index rebuild error: "+err.Error())
	}
	s.refreshRosterAfterDeletion(ctx)
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
	if err := s.cfg.DeletionStore.MarkDeleted(fence.ProjectID, fence.Generation); err != nil {
		return appwire.SessionDeleteResponse{}, appwire.InternalError("commit deleted state: " + err.Error())
	}
	release()
	release = nil
	return s.sessionDeleteResponse(ctx, []string{threadID}, nil, projectID)
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
	response.Navigation = s.navigationAfterDeletion(ctx, hint)
	return response, nil
}
