// Doc-pane data layer (wave 8). Two data paths for the native React doc pane:
//
//   docImageURL - REAL in T1: a pure href builder for /doc/image, which already
//   serves raw image bytes (floor §1.5), so an <img src> can use it directly.
//   The query shape (session + path, both escaped) mirrors the Go handler
//   exactly (cmd/serf-hub/output_images.go:202).
//
//   readDocFile - a rejecting stub in T1; wave-8 T5 fills it against the raw
//   file-content endpoint (/doc/file?format=raw, shipped in main). It returns
//   the client-side DocFileContent model the doc pane renders (binary notice /
//   sanitized markdown / escaped <pre>), NOT the legacy /doc/file HTML page.
export interface DocFileContent {
  text: string;
  binary: boolean;
  mediaType: string;
  truncated: boolean;
  sizeBytes: number;
}

// readDocFile fetches raw file content for the native doc pane. T5 fills it
// (PIN-C: it depends on the controller-owned raw endpoint, not on T5-internal
// state). Rejecting rather than returning an empty shape so a premature caller
// fails loudly instead of silently rendering a blank document.
export function readDocFile(_session: string, _path: string): Promise<DocFileContent> {
  return Promise.reject(new Error("readDocFile: not implemented until wave 8 T5 (raw doc-content endpoint)"));
}

// docImageURL builds the /doc/image href for a session-scoped, cwd-relative
// path. Both query values are escaped, matching the Go handler's own
// url.QueryEscape of sessionID and rel.
export function docImageURL(session: string, path: string): string {
  return `/doc/image?session=${encodeURIComponent(session)}&path=${encodeURIComponent(path)}`;
}
