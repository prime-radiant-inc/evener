// Command serf-hub is the web orchestrator for serf serve daemons.
// It provides a browser-facing UI to discover, drive, and manage many
// concurrent serf serve sessions on the local host.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
)

const Version = "0.1.0"

func main() {
	addr := flag.String("addr", "127.0.0.1:9180", "hub listen address")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serf-hub [flags]\n\nMulti-session web orchestrator for serf serve daemons.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	fmt.Fprintf(os.Stderr, "[hub] serf-hub %s listening on %s\n", Version, *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] %v\n", err)
		os.Exit(1)
	}
}
