// The navigation state layer, published together at
// `@evener/appwire-client/state/navigation`: the resource vocabulary (types),
// the snapshot and delta codec, the graph merge, the deep-freeze helpers the
// codec and merge share, the rule matching an invalidation target to a loaded
// resource (invalidation), the revalidator that re-reads loaded resources on
// the hub's invalidations, the store an app binds its view layer to (store) -
// one hub connection's navigation state, which schedules its own boot work
// and owns navigation's one deadline, the convergence wait - and the graph-
// shaped selectors the web app reads that state through, adoptable by
// native if it ever gains a store (selectors).
//
// Everything a host owns is a port: the client the store reads through and
// where it persists rail expansion. Nothing here touches a framework, the
// DOM or a storage API.
export * from "./codec";
export * from "./immutable";
export * from "./invalidation";
export * from "./merge";
export * from "./revalidator";
export * from "./selectors";
export * from "./store";
export * from "./types";
