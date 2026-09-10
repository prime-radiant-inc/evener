import { AppwireClient } from "@evener/appwire-client";

export function clientFromEnvironment(environment = process.env) {
  const url = environment.EVENER_RPC_URL;
  const cwd = environment.EVENER_CWD;
  if (!url || !cwd) throw new Error("Set EVENER_RPC_URL and EVENER_CWD.");
  return { cwd, hub: new AppwireClient({
    url, clientInfo: { name: "appwire-reference", version: "0.1.0" },
  }) };
}
