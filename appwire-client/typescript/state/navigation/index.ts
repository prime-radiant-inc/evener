// The navigation quartet, published together at
// `@evener/appwire-client/state/navigation`: the resource vocabulary (types),
// the snapshot and delta codec, the graph merge, and the deep-freeze helpers
// the codec and merge share. Nothing here reaches a store, a scheduler or a
// host API; the apps' navigation stores are built on top of it.
export * from "./codec";
export * from "./immutable";
export * from "./merge";
export * from "./types";
