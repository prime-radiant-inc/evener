// Keep this fixture-only server incapable of reaching the live hub, including
// requests a missing FakeClient handler might otherwise leave unnoticed.
import guardConfig from "../browserguard.vite.config.mjs";

export default {
  ...guardConfig,
  server: { ...guardConfig.server, proxy: {} },
};
