import { readFileSync } from "node:fs";
import { AppwireClient } from "@evener/appwire-client";
import { WebSocket } from "ws";
export function clientFromEnvironment() {
  const url = process.env.EVENER_RPC_URL;
  const cwd = process.env.EVENER_CWD;
  if (!url || !cwd) throw new Error("Set EVENER_RPC_URL and EVENER_CWD.");
  const tokenFile = process.env.EVENER_TOKEN_FILE;
  const token = tokenFile ? readFileSync(tokenFile, "utf8").trim() : "";
  return {
    cwd,
    hub: new AppwireClient({
      url,
      clientInfo: { name: "appwire-reference", version: "0.1.0" },
      socketFactory: (endpoint) =>
        new WebSocket(endpoint, {
          headers: token ? { Authorization: `Bearer ${token}` } : {},
        }),
    }),
  };
}
