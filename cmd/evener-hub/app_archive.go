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
	// ref, so it stays unqualified — but it is normalized to the identity its
	// row is read under: a remote row's canonical ref is kept as sent, while a
	// "local:thread" ref collapses to the bare session ID the controller's rows
	// (and every existing local decision) use.
	projectSource := ""
	decisionID := params.ID
	if params.Kind == appwire.ArchiveTargetProject {
		if params.ID == "no-project" {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("no-project is not a local project")
		}
		if err := identifier.ValidateProjectID(params.ID); err != nil {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("invalid project ID: " + err.Error())
		}
		projectSource = hubcore.NormalizeDecisionSource(params.Source)
		if err := validateDecisionSource(cfg, projectSource); err != nil {
			return appwire.ArchiveResponse{}, err
		}
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
	} else {
		// A session's ID is already its host-qualified ref, so the session
		// decision is keyed on the controller source. A non-local source here
		// would address a row no read path consults; reject it instead of
		// silently persisting an inert decision.
		if sessionSource := hubcore.NormalizeDecisionSource(params.Source); sessionSource != "" {
			return appwire.ArchiveResponse{}, appwire.InvalidParams("source is not supported for session archive")
		}
		decisionID = hubcore.NormalizeDecisionSessionID(params.ID)
	}
	if cfg.Archive == nil {
		return appwire.ArchiveResponse{}, appwire.InternalError("archive store not configured")
	}
	if err := cfg.Archive.Set(projectSource, string(params.Kind), decisionID, params.Archived, time.Now()); err != nil {
		return appwire.ArchiveResponse{}, appwire.InternalError("archive store error: " + err.Error())
	}

	// An archive decision can move a session in or out of tier eligibility;
	// nudge the attention watcher so the badge/notification state does not lag
	// behind the sidebar until the next tick, and push the sidebar to refetch.
	// Project scoping is derived from the navigation source's fingerprint delta
	// (the rebuilt tree reflects the decision); navigationChangeHint.Projects is
	// not consumed by commitTargetsLocked, so the handler does not fabricate a
	// per-project hint that no read path honors.
	hint := navigationChangeHint{AllLoadedProjects: params.Kind == appwire.ArchiveTargetSession}
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

// validateDecisionSource rejects a non-empty (already normalized) source that
// does not name a registered remote source. Without this check a typo or
// whitespace variant would persist a successful but permanently inert decision
// row: no read path ever addresses the misspelled source.
//
// A configured host is only a registered source when the hub wired a remote
// client — newHubSourceRegistry skips cfg.RemoteHosts entirely when
// RemoteHostClient is nil — so accepting its name here would persist a decision
// that no source, and no navigation row, can ever address.
func validateDecisionSource(cfg hubcore.WebConfig, source string) error {
	if source == "" {
		return nil
	}
	if cfg.RemoteHostClient == nil {
		return appwire.InvalidParams("unknown source: " + source)
	}
	// The live registry is the authority for hosts that exist past boot: it
	// carries every host the management surface added at runtime, so an
	// archive or favorite decision may name a UI-added host. The configured
	// entries stay accepted alongside it, so without a registry (tests,
	// embedders), or for a name only the config carries, the configured set
	// decides — remoteHostKnown (app_threadlist.go) is the one membership
	// rule this and the fan-out's remote gates share.
	if !remoteHostKnown(cfg, source) {
		return appwire.InvalidParams("unknown source: " + source)
	}
	return nil
}
