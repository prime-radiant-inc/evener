package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/internal/selfupdate"
)

var runHubSelfUpgrade = selfupdate.Upgrade

// hubUpgrade answers evener/upgrade: install the requested channel's build
// without restarting into it (the caller execs manually, unlike
// evener/update/apply). It takes hubUpdateMu so it can't race
// hubUpdateApply over selfupdate's fixed .tmp install path, and always
// releases it before returning since it never execs.
func hubUpgrade(ctx context.Context, params appwire.UpgradeParams) (appwire.UpgradeResponse, error) {
	if err := tryLockHubUpdate(); err != nil {
		return appwire.UpgradeResponse{}, err
	}
	defer hubUpdateMu.Unlock()

	prefix, binDir, shareBinDir := hubInstallDirs()
	result, err := runHubSelfUpgradeWithTimeout(ctx, selfupdate.Options{
		Requested:      params.Requested,
		CurrentChannel: buildinfo.UpgradeChannel(),
		Prefix:         prefix,
		BinDir:         binDir,
		ShareBinDir:    shareBinDir,
	})
	if err != nil {
		return appwire.UpgradeResponse{}, err
	}
	return appwire.UpgradeResponse{
		Release:        result.Release,
		Channel:        result.Channel,
		URL:            result.URL,
		Archive:        result.Archive,
		Prefix:         result.Prefix,
		BinDir:         result.BinDir,
		ShareBinDir:    result.ShareBinDir,
		Installed:      result.Installed,
		RestartMessage: result.RestartMessage,
	}, nil
}
