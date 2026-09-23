// The task-card mock-ups harness's own dev-server config: the base config
// verbatim, plus the two remote-access knobs the base deliberately leaves
// off (its server.host is 127.0.0.1, and Vite rejects any non-localhost Host
// header by default). Jesse reviews these mock-ups over tailscale at
// http://magic-kingdom:<port>/taskcardmockups.html, so the harness runs as
// `vite dev --config taskcardmockups.vite.config.ts --port <port>`.
// Dev-support scaffolding, not production config.
import base from "./vite.config";

export default {
  ...base,
  server: { ...base.server, host: true, allowedHosts: ["magic-kingdom", "localhost"] },
};
