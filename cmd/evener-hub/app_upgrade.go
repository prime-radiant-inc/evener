package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/internal/selfupdate"
)

var runHubSelfUpgrade = selfupdate.Upgrade

func hubUpgrade(ctx context.Context, params appwire.UpgradeParams) (appwire.UpgradeResponse, error) {
	result, err := runHubSelfUpgrade(ctx, selfupdate.Options{
		Requested:      params.Requested,
		CurrentChannel: buildinfo.UpgradeChannel(),
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
