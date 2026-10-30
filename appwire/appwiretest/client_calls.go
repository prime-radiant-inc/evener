package appwiretest

import (
	"context"

	"primeradiant.com/evener/appwire"
)

// Typed calls over appwire.Client.Request for tests that drive a real hub or
// daemon through the client. Production code issues these methods through
// Client.Request directly, so none of them live on Client.

func call[R any](ctx context.Context, c *appwire.Client, method string, params any) (R, error) {
	var out R
	err := c.Request(ctx, method, params, &out)
	return out, err
}

func ThreadStart(ctx context.Context, c *appwire.Client, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return call[appwire.ThreadStartResponse](ctx, c, appwire.MethodThreadStart, params)
}

func ThreadResume(ctx context.Context, c *appwire.Client, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return call[appwire.ThreadResumeResponse](ctx, c, appwire.MethodThreadResume, params)
}

func ThreadFork(ctx context.Context, c *appwire.Client, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return call[appwire.ThreadForkResponse](ctx, c, appwire.MethodThreadFork, params)
}

func ThreadTranscriptList(ctx context.Context, c *appwire.Client, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
	return call[appwire.ThreadTranscriptListResponse](ctx, c, appwire.MethodEvenerThreadTranscriptsList, params)
}

func AuthStatus(ctx context.Context, c *appwire.Client, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	return call[appwire.AuthStatusResponse](ctx, c, appwire.MethodEvenerAuthStatus, params)
}

func AuthLogout(ctx context.Context, c *appwire.Client, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	return call[appwire.AuthLogoutResponse](ctx, c, appwire.MethodEvenerAuthLogout, params)
}

func DirsCreate(ctx context.Context, c *appwire.Client, params appwire.DirsCreateParams) (appwire.DirsCreateResponse, error) {
	return call[appwire.DirsCreateResponse](ctx, c, appwire.MethodEvenerDirsCreate, params)
}

func PathsComplete(ctx context.Context, c *appwire.Client, params appwire.PathsCompleteParams) (appwire.PathsCompleteResponse, error) {
	return call[appwire.PathsCompleteResponse](ctx, c, appwire.MethodEvenerPathsComplete, params)
}

func ProjectsRecent(ctx context.Context, c *appwire.Client, params appwire.ProjectsRecentParams) (appwire.ProjectsRecentResponse, error) {
	return call[appwire.ProjectsRecentResponse](ctx, c, appwire.MethodEvenerProjectsRecent, params)
}

func ProjectDelete(ctx context.Context, c *appwire.Client, params appwire.ProjectDeleteParams) (appwire.ProjectDeleteResponse, error) {
	return call[appwire.ProjectDeleteResponse](ctx, c, appwire.MethodEvenerProjectDelete, params)
}

func NavigationRead(ctx context.Context, c *appwire.Client, params appwire.NavigationReadParams) (appwire.NavigationReadResponse, error) {
	return call[appwire.NavigationReadResponse](ctx, c, appwire.MethodEvenerNavigationRead, params)
}

func GitHead(ctx context.Context, c *appwire.Client, params appwire.GitHeadParams) (appwire.GitHeadResponse, error) {
	return call[appwire.GitHeadResponse](ctx, c, appwire.MethodEvenerGitHead, params)
}

func ArchiveSet(ctx context.Context, c *appwire.Client, params appwire.ArchiveParams) (appwire.ArchiveResponse, error) {
	return call[appwire.ArchiveResponse](ctx, c, appwire.MethodEvenerArchiveSet, params)
}

func UpdateApply(ctx context.Context, c *appwire.Client, params appwire.UpdateApplyParams) (appwire.UpdateApplyResponse, error) {
	return call[appwire.UpdateApplyResponse](ctx, c, appwire.MethodEvenerUpdateApply, params)
}

func MarketplaceAdd(ctx context.Context, c *appwire.Client, params appwire.MarketplaceAddParams) (appwire.MarketplaceListResponse, error) {
	return call[appwire.MarketplaceListResponse](ctx, c, appwire.MethodEvenerMarketplaceAdd, params)
}

func PluginPreview(ctx context.Context, c *appwire.Client, params appwire.PluginPreviewParams) (appwire.PluginPreviewResponse, error) {
	return call[appwire.PluginPreviewResponse](ctx, c, appwire.MethodEvenerPluginPreview, params)
}

func SpawnSlashCatalog(ctx context.Context, c *appwire.Client, params appwire.SpawnSlashCatalogParams) (appwire.SpawnSlashCatalogResponse, error) {
	return call[appwire.SpawnSlashCatalogResponse](ctx, c, appwire.MethodEvenerSpawnSlashCatalog, params)
}

func TurnSteer(ctx context.Context, c *appwire.Client, params appwire.TurnSteerParams) error {
	return c.Request(ctx, appwire.MethodTurnSteer, params, nil)
}

func TurnInterrupt(ctx context.Context, c *appwire.Client, params appwire.TurnInterruptParams) error {
	return c.Request(ctx, appwire.MethodTurnInterrupt, params, nil)
}

func TurnQueue(ctx context.Context, c *appwire.Client, params appwire.TurnQueueParams) error {
	return c.Request(ctx, appwire.MethodTurnQueue, params, nil)
}

func TurnDrainAsSteer(ctx context.Context, c *appwire.Client, params appwire.TurnDrainAsSteerParams) error {
	return c.Request(ctx, appwire.MethodTurnDrainAsSteer, params, nil)
}

func TurnPromoteQueuedAsSteer(ctx context.Context, c *appwire.Client, params appwire.TurnPromoteQueuedAsSteerParams) error {
	return c.Request(ctx, appwire.MethodTurnPromoteQueuedAsSteer, params, nil)
}

func CommandList(ctx context.Context, c *appwire.Client) (appwire.CommandListResponse, error) {
	return call[appwire.CommandListResponse](ctx, c, appwire.MethodEvenerCommandList, appwire.EmptyParams{})
}
