import type { DocPort } from "../../protocol/docContent";

// The browser's doc seam. The empty origin is the point of it: the page is
// served by the hub, so every /doc URL stays the same-origin path this app has
// always requested, and the auth cookie rides along under same-origin
// credentials exactly as the manifest and every other fetch here does.
// readDocFile takes this as a port, so the shared package names neither the
// fetch global, nor an origin, nor a credentials policy; the host that has a
// cookie jar supplies all three.
export const browserDocPort: DocPort = {
  origin: "",
  fetch: (url) => fetch(url, { credentials: "same-origin" }),
};
