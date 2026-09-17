// The extensions state layer, published at
// `@evener/appwire-client/state/extensions`: the framework-free stores behind
// both apps' plugin marketplace and installed-plugin settings.
// Each is a factory taking a narrow client port; the apps build the instances
// they wire to their view layers.
export * from "./keyedRevision";
export * from "./launchLayer";
export * from "./listRevision";
export * from "./marketplaces";
export * from "./plugins";
export * from "./storeLifecycle";
