package appsource

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"primeradiant.com/evener/appwire"
)

type appwireDialFunc func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error)

func defaultAppwireDial(ctx context.Context, endpoint string, client *http.Client, header http.Header) (appwire.Transport, error) {
	return appwire.DialWebSocketWithHeaders(ctx, endpoint, client, header)
}

// hubStderr is captured once because runClientKeepalive outlives the test
// that started it (connect uses context.WithoutCancel), so reading the
// mutable os.Stderr global from that goroutine races any test that swaps
// and restores os.Stderr (issue #837).
var hubStderr = os.Stderr

// hubConnectionLogf is the appwire.Client connection-lifecycle sink (see
// appwire.Client.SetLogf) for all hub-dialed source connections. The hub is a
// plain daemon, never a TUI rendering over an interactive terminal, so its own
// stderr — labelled like every other hub diagnostic — is a safe destination,
// unlike the TUI's stderr (issue #783).
func hubConnectionLogf(format string, args ...any) {
	_, _ = fmt.Fprintf(hubStderr, "[hub] "+format+"\n", args...)
}
