package hub

import (
	"context"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
)

func registerArchiveHandler(server *appserver.Server, cfg hubcore.WebConfig, navigation func() *NavigationService) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerArchiveSet, func(ctx context.Context, params appwire.ArchiveParams) (appwire.ArchiveResponse, error) {
		return archiveSet(ctx, cfg, navigation(), params)
	})
}

func archiveSet(ctx context.Context, cfg hubcore.WebConfig, navigation *NavigationService, params appwire.ArchiveParams) (appwire.ArchiveResponse, error) {
	switch params.Kind {
	case appwire.ArchiveTargetSession, appwire.ArchiveTargetProject:
	default:
		return appwire.ArchiveResponse{}, appwire.InvalidParams(`kind must be "session" or "project"`)
	}
	if params.ID == "" {
		return appwire.ArchiveResponse{}, appwire.InvalidParams("id is required")
	}
	if params.Kind == appwire.ArchiveTargetProject {
		if params.ID == "no-project" {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("no-project is not a local project")
		}
		if err := identifier.ValidateProjectID(params.ID); err != nil {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("invalid project ID: " + err.Error())
		}
		if params.WorkingDir == "" {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("workingDir is required for project archive")
		}
		project, err := identifier.ResolveProject(params.WorkingDir)
		if err != nil {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("resolve project: " + err.Error())
		}
		if project.ID != params.ID {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("project ID does not match workingDir")
		}
	}
	if cfg.Archive == nil {
		return appwire.ArchiveResponse{}, appwire.InternalError("archive store not configured")
	}
	if err := cfg.Archive.Set(string(params.Kind), params.ID, params.Archived, time.Now()); err != nil {
		return appwire.ArchiveResponse{}, appwire.InternalError("archive store error: " + err.Error())
	}

	// An archive decision can move a session in or out of tier eligibility;
	// nudge the attention watcher so the badge/notification state does not lag
	// behind the sidebar until the next tick, and push the sidebar to refetch.
	hint := navigationChangeHint{AllLoadedProjects: params.Kind == appwire.ArchiveTargetSession}
	if params.Kind == appwire.ArchiveTargetProject {
		hint.Projects = []string{params.ID}
	}
	if navigation == nil {
		return appwire.ArchiveResponse{}, appwire.Unavailable("navigation unavailable")
	}
	navigationMutation, err := navigation.Refresh(ctx, hint)
	if err != nil {
		return appwire.ArchiveResponse{}, appwire.Unavailable(err.Error())
	}
	if cfg.PokeAttention != nil {
		cfg.PokeAttention()
	}
	return appwire.ArchiveResponse{OK: true, Navigation: navigationMutation}, nil
}
