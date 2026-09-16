// The navigation state layer, published together at
// `@evener/appwire-client/state/navigation`: the resource vocabulary (types),
// the snapshot and delta codec, the graph merge, the deep-freeze helpers the
// codec and merge share, the rule matching an invalidation target to a loaded
// resource (invalidation), and the revalidator that re-reads loaded resources
// on the hub's invalidations. Nothing here reaches a store, a scheduler or a
// host API; the apps' navigation stores are built on top of it.
export * from "./codec";
export * from "./immutable";
export * from "./invalidation";
export * from "./merge";
export * from "./revalidator";
export * from "./selectors";
export * from "./store";
export * from "./types";
