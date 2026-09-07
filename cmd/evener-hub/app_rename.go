package hub

import (
	"context"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

var (
	loadSessionMetaForRename = schema.LoadSessionMeta
	saveSessionMetaForRename = schema.SaveSessionMeta
)

type threadNameMutation struct {
	projectKey string
}

func registerThreadNameSetHandler(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadNameSet, func(ctx context.Context, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
		return setThreadName(ctx, cfg, sources, navigation, params)
	})
}

func setThreadName(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
	}
	params.Ref = ref.String()
	params.Name = strings.TrimSpace(params.Name)
	if params.Name == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("name is required")
	}

	mutation, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", "", func() (threadNameMutation, error) {
		if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, "", ""); err != nil {
			return threadNameMutation{}, err
		}
		return mutateThreadName(ctx, cfg, sources, ref, params)
	})
	if err != nil {
		return appwire.EmptyResponse{}, err
	}
	completeThreadNameMutation(cfg, navigation, mutation)
	return appwire.EmptyResponse{}, nil
}

func mutateThreadName(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, ref appwire.Ref, params appwire.ThreadNameSetParams) (threadNameMutation, error) {
	if ref.SourceID != "local" || threadNameIsLive(cfg, ref.ThreadID) || cfg.Past == nil {
		return renameLiveThread(ctx, cfg, sources, ref, params)
	}
	entry, ok := cfg.Past.Find(ref.ThreadID)
	if !ok {
		return renameLiveThread(ctx, cfg, sources, ref, params)
	}
	meta, err := loadSessionMetaForRename(entry.StateDir, entry.ID)
	if err != nil {
		return threadNameMutation{}, appwire.InternalError("load meta: " + err.Error())
	}
	if threadNameIsLive(cfg, ref.ThreadID) {
		return renameLiveThread(ctx, cfg, sources, ref, params)
	}
	meta.Name = params.Name
	meta.NameSource = "user"
	meta.NameUpdatedAt = time.Now().UTC()
	if err := saveSessionMetaForRename(entry.StateDir, meta); err != nil {
		return threadNameMutation{}, appwire.InternalError("save meta: " + err.Error())
	}
	// UpdateMeta keeps the past index entry coherent with the saved meta; the
	// OnChange notification it fires is consumed by the main wiring (see
	// main.go), not by the rename completion below.
	cfg.Past.UpdateMeta(entry.ID, meta)
	return threadNameMutation{projectKey: projectKeyForStateDir(entry.StateDir)}, nil
}

func renameLiveThread(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, ref appwire.Ref, params appwire.ThreadNameSetParams) (threadNameMutation, error) {
	source, err := sourceForThread(sources, params.Ref, "")
	if err != nil {
		return threadNameMutation{}, appwire.Unavailable(err.Error())
	}
	if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "rename"); err != nil {
		return threadNameMutation{}, err
	}
	if err := source.SetThreadName(ctx, params); err != nil {
		return threadNameMutation{}, err
	}
	return refreshThreadNameMeta(cfg, ref, params.Name), nil
}

func refreshThreadNameMeta(cfg hubcore.WebConfig, ref appwire.Ref, name string) threadNameMutation {
	mutation := threadNameMutation{}
	if ref.SourceID != "local" || cfg.Past == nil {
		return mutation
	}
	entry, ok := cfg.Past.Find(ref.ThreadID)
	if !ok {
		return mutation
	}
	mutation.projectKey = projectKeyForStateDir(entry.StateDir)
	meta, err := loadSessionMetaForRename(entry.StateDir, entry.ID)
	if err != nil {
		meta = entry.Meta
		meta.Name = name
		meta.NameSource = "user"
	}
	cfg.Past.UpdateMeta(entry.ID, meta)
	return mutation
}

func completeThreadNameMutation(cfg hubcore.WebConfig, navigation *NavigationService, mutation threadNameMutation) {
	pokeMutationAttention(cfg)
	if navigation == nil {
		return
	}
	hint := navigationChangeHint{AllLoadedProjects: true}
	if mutation.projectKey != "" {
		hint = navigationChangeHint{Projects: []string{mutation.projectKey}}
	}
	// The rename is durably committed above; Invalidate queues the rebuild off
	// the RPC path (see Invalidate/refreshPending for the retry contract).
	navigation.Invalidate(hint)
}

func threadNameIsLive(cfg hubcore.WebConfig, threadID string) bool {
	if cfg.Roster == nil {
		return false
	}
	_, live := cfg.Roster.Find(threadID)
	return live
}
