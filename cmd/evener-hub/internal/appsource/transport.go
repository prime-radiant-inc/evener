package appsource

import (
	"context"
	"net/http"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/execsupport/componentlog"
)

type appwireDialFunc func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error)

func defaultAppwireDial(ctx context.Context, endpoint string, client *http.Client, header http.Header) (appwire.Transport, error) {
	return appwire.DialWebSocketWithHeaders(ctx, endpoint, client, header)
}

// hubLog captures stderr once because runClientKeepalive outlives the test
// that started it (connect uses context.WithoutCancel), so reading the
// mutable os.Stderr global from that goroutine races any test that swaps
// and restores os.Stderr (issue #837).
var hubLog = componentlog.New("hub")
