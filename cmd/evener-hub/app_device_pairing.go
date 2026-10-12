package hub

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

func registerDevicePairingHandlers(server *appserver.Server, p *devicePairings) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDevicePairingCreate, func(_ context.Context, v appwire.DevicePairingCreateParams) (appwire.DevicePairingInvitation, error) {
		return p.create(v)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDevicePairingRead, func(_ context.Context, v appwire.DevicePairingParams) (appwire.DevicePairingStatus, error) {
		return p.read(v)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDevicePairingApprove, func(_ context.Context, v appwire.DevicePairingApproveParams) (appwire.DevicePairingStatus, error) {
		return p.approve(v)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDevicePairingCancel, func(_ context.Context, v appwire.DevicePairingParams) (appwire.DevicePairingStatus, error) {
		return p.cancel(v)
	})
}

// This sole unauthenticated bootstrap never receives a durable credential.
// Responses carry it only after authenticated approval and inside a NaCl box.
func (p *devicePairings) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" || r.Header.Get("Origin") != "" {
		http.Error(w, "native JSON request required", http.StatusBadRequest)
		return
	}
	var params appwire.DevicePairingClaimParams
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&params) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid claim", http.StatusBadRequest)
		return
	}
	response, status := p.claim(params)
	if status != 200 {
		http.Error(w, "claim unavailable", status)
		return
	}
	writeAPIJSON(w, http.StatusOK, response)
}
