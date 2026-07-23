// Pure presentation helpers for the doc pane: a file's display name, whether
// it renders as markdown, and a human-readable byte size. Kept in their own
// leaf module so they unit-test without mounting the pane (and so the tiny
// registration module can compute a tab title without importing the pane).

// filenameOf returns the last segment of a cwd-relative slash path - the tab
// title and the binary/error notices all show the bare filename, never the
// full path.
export function filenameOf(path: string): string {
  return (
    path
      .split("/")
      .filter((segment) => segment.length > 0)
      .at(-1) ?? path
  );
}

// isMarkdownPath reports whether a path renders as markdown: a case-insensitive
// .md or .markdown extension ONLY (floor §1.3:239 - the same rule the Go
// handler applies via strings.EqualFold on filepath.Ext).
export function isMarkdownPath(path: string): boolean {
  return /\.(?:md|markdown)$/i.test(path);
}

// formatDocBytes renders a byte count the way the Go handler's formatDocBytes
// does (cmd/serf-hub/doc_serve.go:223): floored binary units, B / KiB / MiB.
export function formatDocBytes(n: number): string {
  if (n >= 1 << 20) return `${Math.floor(n / (1 << 20))} MiB`;
  if (n >= 1 << 10) return `${Math.floor(n / (1 << 10))} KiB`;
  return `${n} B`;
}
