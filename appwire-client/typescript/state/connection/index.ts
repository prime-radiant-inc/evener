// The connection state layer, published as
// `@evener/appwire-client/state/connection`: the client-swap safety and
// notification-following that follow whichever AppwireClientLike a host is
// wired to. The host's own view binding (zustand, React) and its handshake
// read (serverInfo/features) stay in the app.
export type { ConnectionStore, ConnectionStoreState } from "./core";
export { createConnectionStore, onConnectionNotification } from "./core";
