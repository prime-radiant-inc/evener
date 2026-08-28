package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// fetchStatus reads /status from the daemon at le.Address, returning nil on any error.
func (s *WebServer) fetchStatus(le hubcore.LiveEntry) *daemonStatus {
	client := &http.Client{Timeout: 1 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+le.Address+"/status", nil) //nolint:gosec
	if err != nil {
		return nil
	}
	hubcore.SetDaemonAuthorization(req.Header, le.HubToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on read path; error is not actionable
	var info daemonStatus
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil
	}
	return &info
}
