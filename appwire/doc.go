// Package appwire defines the JSON-RPC wire protocol shared by the evener
// binaries. AppWire connects Evener clients, hubs, and session sources. It carries
// the message envelope (request/response/notification), the request-method and
// notification catalogs, the param/result types, and the WebSocket transport
// with its keepalive contract.
//
// The protocol reference doc (docs/appwire-protocol.md) and the frontend's
// TypeScript protocol types (cmd/evener-hub/frontend/src/protocol/types.gen.ts)
// are both generated from the declarative catalog below (Methods,
// Notifications) via `go generate`; see protocol.go. Both committed outputs
// are verified current in CI, so the catalog in code is the single source of
// truth.
//
// Field names on the wire are camelCase, and the json tags here are where
// that is decided: appwirets emits the frontend's TypeScript property names
// from these tag strings verbatim, so a tag is simultaneously the wire name
// every client sends and the name the web UI reads. Renaming one breaks both
// at once, which is why the repo's snake_case tag lint is excluded for the
// files that mirror this format rather than applied to them.
//
// Decode frames into the types below rather than redeclaring their shapes.
// A local copy compiles, passes its own tests, and then drifts silently when
// a field is added here; it also reintroduces the casing question in a
// package that has no wire obligation, where the answer looks like a lint
// suppression instead of a contract. If a caller needs a subset, use the
// params type that already carries those fields.
//
//go:generate go run primeradiant.com/evener/internal/appwiredoc -out ../docs/appwire-protocol.md
//go:generate go run primeradiant.com/evener/internal/appwirets -out ../cmd/evener-hub/frontend/src/protocol/types.gen.ts
package appwire
