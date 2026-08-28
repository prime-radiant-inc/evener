package hub

import (
	"context"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func registerFavoriteHandler(server *appserver.Server, cfg hubcore.WebConfig, navigation *NavigationService) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerFavoriteSet, func(ctx context.Context, params appwire.FavoriteSetParams) (appwire.FavoriteSetResponse, error) {
		if params.Kind == "session" {
			return appwire.FavoriteSetResponse{}, appwire.InvalidParams("session favorites use evener/session-pin/assign")
		}
		if params.Kind != "project" {
			return appwire.FavoriteSetResponse{}, appwire.InvalidParams(`kind must be "project"`)
		}
		if params.ID == "" {
			return appwire.FavoriteSetResponse{}, appwire.InvalidParams("id is required")
		}
		if cfg.Favorite == nil {
			return appwire.FavoriteSetResponse{}, appwire.InternalError("favorite store not configured")
		}
		if err := cfg.Favorite.Set(params.Kind, params.ID, params.Favorited, time.Now()); err != nil {
			return appwire.FavoriteSetResponse{}, appwire.InternalError("favorite store error: " + err.Error())
		}
		if navigation == nil {
			return appwire.FavoriteSetResponse{}, appwire.Unavailable("navigation service not configured")
		}
		mutation, err := navigation.Refresh(ctx, navigationChangeHint{Projects: []string{params.ID}})
		if err != nil {
			return appwire.FavoriteSetResponse{}, appwire.Unavailable(err.Error())
		}
		pokeMutationAttention(cfg)
		return appwire.FavoriteSetResponse{OK: true, Navigation: mutation}, nil
	})
}
