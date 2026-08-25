import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/App";
import "./styles/base.css";
import "./styles/foundation.css";

const root = document.getElementById("root");
if (!root) throw new Error("missing #root mount point");

const allowOverride =
  import.meta.env.DEV ||
  import.meta.env.MODE === "test" ||
  import.meta.env.MODE === "browser-test";
const override = allowOverride
  ? new URLSearchParams(window.location.search).get("platform")
  : null;

createRoot(root).render(
  <StrictMode>
    <App
      platformInput={{
        userAgent: navigator.userAgent,
        navigatorPlatform: navigator.platform,
        maxTouchPoints: navigator.maxTouchPoints,
        allowOverride,
        override,
        fallback: __TAURI_BUILD_PLATFORM__,
      }}
      storage={window.localStorage}
    />
  </StrictMode>,
);
