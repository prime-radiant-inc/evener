// Package appwire defines the JSON-RPC wire protocol shared by the serf
// binaries: the browser and serf-tui speak it to serf-hub, and serf-hub speaks
// it to each serf serve daemon (and to Codex app-server sources). It carries
// the message envelope (request/response/notification), the request-method and
// notification catalogs, the param/result types, and the WebSocket transport
// with its keepalive contract.
//
// The protocol reference doc (docs/appwire-protocol.md) and the frontend's
// TypeScript protocol types (cmd/serf-hub/frontend/src/protocol/types.gen.ts)
// are both generated from the declarative catalog below (Methods,
// Notifications) via `go generate`; see protocol.go. Both committed outputs
// are verified current in CI, so the catalog in code is the single source of
// truth.
//
//go:generate go run primeradiant.com/serf/internal/appwiredoc -out ../docs/appwire-protocol.md
//go:generate go run primeradiant.com/serf/internal/appwirets -out ../cmd/serf-hub/frontend/src/protocol/types.gen.ts
package appwire
