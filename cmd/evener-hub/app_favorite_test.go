package hub

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func dispatchFavoriteSet(t *testing.T, server *appserver.Server, params appwire.FavoriteSetParams) (appwire.FavoriteSetResponse, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal favorite params: %v", err)
	}
	result, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerFavoriteSet,
		Params: raw,
	})
	if err != nil {
		return appwire.FavoriteSetResponse{}, err
	}
	response, ok := result.(appwire.FavoriteSetResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.FavoriteSetResponse", result)
	}
	return response, nil
}

func TestHubFavoriteSetPersistsProjectAndPublishesNavigation(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	navigation := newTestNavigationService(t, source)
	if _, err := navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.changeTitle("changed")

	attentionCalls := 0
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "favorites.db"))
	server := newHubAppServerWithNavigation(hubcore.WebConfig{
		Favorite: favorites,
		PokeAttention: func() {
			attentionCalls++
		},
	}, nil, navigation)

	response, err := dispatchFavoriteSet(t, server, appwire.FavoriteSetParams{
		Kind:      "project",
		ID:        "p1",
		Favorited: true,
	})
	if err != nil {
		t.Fatalf("favorite set: %v", err)
	}
	if !response.OK {
		t.Fatalf("response=%+v, want ok", response)
	}
	decisions, err := favorites.Favorites()
	if err != nil {
		t.Fatalf("favorites: %v", err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "project", ID: "p1"}] {
		t.Fatalf("favorite not persisted: %v", decisions)
	}
	if attentionCalls != 1 {
		t.Fatalf("attention calls=%d, want 1", attentionCalls)
	}

	publications := navigation.DrainPublications()
	if len(publications) != 1 {
		t.Fatalf("publications=%+v, want one navigation publication", publications)
	}
	if response.Navigation.GenerationID != publications[0].GenerationID || !reflect.DeepEqual(response.Navigation.Targets, publications[0].Targets) {
		t.Fatalf("response navigation=%+v, publication=%+v", response.Navigation, publications[0])
	}
	if len(response.Navigation.Targets) != 1 || response.Navigation.Targets[0].Kind != appwire.NavigationTargetProject || response.Navigation.Targets[0].ProjectKey != "p1" {
		t.Fatalf("navigation targets=%+v, want the changed project", response.Navigation.Targets)
	}
}

func TestHubFavoriteSetRejectsObsoleteOrInvalidKinds(t *testing.T) {
	server := newHubAppServerWithNavigation(hubcore.WebConfig{Favorite: hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "favorites.db"))}, nil, nil)
	tests := []struct {
		name    string
		params  appwire.FavoriteSetParams
		message string
	}{
		{
			name:    "session",
			params:  appwire.FavoriteSetParams{Kind: "session", ID: "local:s1", Favorited: true},
			message: "evener/session-pin/assign",
		},
		{
			name:    "unknown kind",
			params:  appwire.FavoriteSetParams{Kind: "widget", ID: "x", Favorited: true},
			message: `kind must be "project"`,
		},
		{
			name:    "empty ID",
			params:  appwire.FavoriteSetParams{Kind: "project", Favorited: true},
			message: "id is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := dispatchFavoriteSet(t, server, tt.params)
			assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
			if !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("error=%q, want %q", err, tt.message)
			}
		})
	}
}

func TestHubFavoriteSetRequiresFavoriteStore(t *testing.T) {
	server := newHubAppServerWithNavigation(hubcore.WebConfig{}, nil, nil)
	_, err := dispatchFavoriteSet(t, server, appwire.FavoriteSetParams{Kind: "project", ID: "p1", Favorited: true})
	if err == nil {
		t.Fatal("favorite set succeeded without a favorite store")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError || !strings.Contains(wire.Message, "favorite store not configured") {
		t.Fatalf("error=%T %v, want internal favorite-store error", err, err)
	}
}
