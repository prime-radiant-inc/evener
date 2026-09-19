package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/interactiveartifacts"
	"primeradiant.com/evener/rendezvous"
)

func rebootstrapLiveDaemon(ctx context.Context, authority *interactiveartifacts.HostAuthority, hubEpoch string, entry rendezvous.Entry) (*interactiveartifacts.HubBrokerConnection, error) {
	if authority == nil || hubEpoch == "" || strings.TrimSpace(entry.HubToken) == "" {
		return nil, interactiveartifacts.ErrBrokerAuthentication
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("artifact broker redirect refused")
		},
	}
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+entry.HubToken)
	transport, err := appwire.DialPrivateWebSocketWithHeaders(ctx, "ws://"+entry.Address+appwire.PrivateBrokerPath, client, header)
	if err != nil {
		return nil, fmt.Errorf("dial private artifact broker: %w", err)
	}
	connection, err := interactiveartifacts.EstablishHubRebootstrap(ctx, interactiveartifacts.HubRebootstrapConfig{
		Transport: transport, Authority: authority, HubEpoch: hubEpoch,
		ExpectedIdentity: interactiveartifacts.DaemonIdentity(entry),
	})
	if err != nil {
		_ = transport.Close()
		return nil, err
	}
	return connection, nil
}
