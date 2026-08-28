package hub

import (
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

func registerProjectDeleteHandler(server *appserver.Server, web *WebServer) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerProjectDelete, web.projectDelete)
}
