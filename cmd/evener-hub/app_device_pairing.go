package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"time"

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
	control := http.NewResponseController(w)
	_ = control.SetReadDeadline(time.Now().Add(5 * time.Second))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		w.Header().Set("Connection", "close")
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" || r.Header.Get("Origin") != "" {
		w.Header().Set("Connection", "close")
		http.Error(w, "native JSON request required", http.StatusBadRequest)
		return
	}
	var params appwire.DevicePairingClaimParams
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&params)
	if err == nil {
		err = decoder.Decode(new(any))
		if errors.Is(err, io.EOF) {
			err = nil
		} else if err == nil {
			err = errors.New("trailing claim value")
		}
	}
	if err != nil {
		// Do not drain an unfinished body or reuse its connection after refusal.
		w.Header().Set("Connection", "close")
		status := http.StatusBadRequest
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			status = http.StatusRequestTimeout
		}
		http.Error(w, "invalid claim", status)
		return
	}
	_ = control.SetReadDeadline(time.Time{})
	response, status := p.claim(params)
	if status != 200 {
		http.Error(w, "claim unavailable", status)
		return
	}
	writeAPIJSON(w, http.StatusOK, response)
}
