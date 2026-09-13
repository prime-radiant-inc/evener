import type { DocFetch } from "../../protocol/docContent";

// The browser's doc transport. same-origin credentials so the hub's auth
// cookie rides along, exactly as the manifest and every other same-origin
// fetch in this app do. readDocFile takes this as a port, so the shared
// package names neither the fetch global nor a credentials policy; the host
// that has a cookie jar supplies both.
export const browserDocFetch: DocFetch = (url) => fetch(url, { credentials: "same-origin" });
