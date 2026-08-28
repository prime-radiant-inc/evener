package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

func registerProjectDeleteHandler(server *appserver.Server, web *WebServer) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerProjectDelete, func(ctx context.Context, params appwire.ProjectDeleteParams) (appwire.ProjectDeleteResponse, error) {
		return web.projectDelete(ctx, params)
	})
}
