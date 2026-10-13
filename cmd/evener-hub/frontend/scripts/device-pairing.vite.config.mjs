import { mergeConfig } from "vite";
import base from "./browserguard.vite.config.mjs";

const target = process.env.EVENER_PAIRING_E2E_URL;
if (!target || new URL(target).hostname !== "127.0.0.1")
  throw new Error("Pairing check needs an isolated loopback Hub.");
export default mergeConfig(base, {
  server: {
    proxy: {
      "/rpc": {
        target,
        ws: true,
        changeOrigin: true,
        headers: { Authorization: "Bearer synthetic-pairing-token", Origin: target },
      },
    },
  },
});
