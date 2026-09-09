package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func hubSpawnSlashCatalog(_ context.Context, _ hubcore.WebConfig, _ appwire.SpawnSlashCatalogParams) (appwire.SpawnSlashCatalogResponse, error) {
	return appwire.SpawnSlashCatalogResponse{Commands: []appwire.CommandDescriptor{}}, nil
}
