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
	// The owning source qualifies a project decision: two hosts' projects share
	// a project ID (and often a path), so keying by (source, id) keeps their
	// archive decisions distinct. A session's ID is already its host-qualified
	// ref, so it stays unqualified.
	projectSource := ""
	if params.Kind == appwire.ArchiveTargetProject {
		if params.ID == "no-project" {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("no-project is not a local project")
		}
		if err := identifier.ValidateProjectID(params.ID); err != nil {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("invalid project ID: " + err.Error())
		}
		projectSource = hubcore.NormalizeDecisionSource(params.Source)
		if projectSource == "" {
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
		} else if err := validateHostProjectArchive(cfg, projectSource, params.ID, params.WorkingDir); err != nil {
			return appwire.ArchiveResponse{}, err
		}
	}
	if cfg.Archive == nil {
		return appwire.ArchiveResponse{}, appwire.InternalError("archive store not configured")
	}
	if err := cfg.Archive.Set(projectSource, string(params.Kind), params.ID, params.Archived, time.Now()); err != nil {
		return appwire.ArchiveResponse{}, appwire.InternalError("archive store error: " + err.Error())
	}

	// An archive decision can move a session in or out of tier eligibility;
	// nudge the attention watcher so the badge/notification state does not lag
	// behind the sidebar until the next tick, and push the sidebar to refetch.
	hint := navigationChangeHint{AllLoadedProjects: params.Kind == appwire.ArchiveTargetSession}
	if params.Kind == appwire.ArchiveTargetProject {
		hint.Projects = []string{projectsChangeKey(projectSource, params.ID)}
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

// validateHostProjectArchive cross-checks a non-local project archive against
// the identity the remote hub already reported for its row. The controller
// never resolves the host path against its own filesystem: params.WorkingDir is
// optional and, when present, must agree with the path the host reported for
// that project ID. A cold or absent remote cache has no reported rows to
// compare, so the ID's syntax is the only check — the (source, id) store key,
// not a controller-side resolution, is what makes the action unambiguous.
func validateHostProjectArchive(cfg hubcore.WebConfig, source, id, workingDir string) error {
	if cfg.RemoteThreadCache == nil {
		return nil
	}
	for _, thread := range cfg.RemoteThreadCache.Get() {
		if thread.Source != source {
			continue
		}
		if thread.ProjectID == id {
			if workingDir != "" && thread.ProjectPath != "" && thread.ProjectPath != workingDir {
				return appwire.InvalidParams("project ID does not match workingDir")
			}
			return nil
		}
		if workingDir != "" && thread.ProjectPath == workingDir {
			return appwire.InvalidParams("project ID does not match workingDir")
		}
	}
	return nil
}

// projectsChangeKey qualifies a project navigation change hint by its owning
// source, so a poke for one host's project cannot refresh another host's
// project that happens to share an ID. The controller's own projects keep their
// bare ID.
func projectsChangeKey(source, id string) string {
	if source == "" {
		return id
	}
	return source + ":" + id
}
