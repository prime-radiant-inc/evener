// Opening a URL in a new tab must not leave the new document with
// `window.opener`: an opener-created window copies this tab's sessionStorage -
// the per-client mutation identity lives there (stores/mutationClientIdentity.ts),
// and a shared identity would let both tabs claim each other's durable sends.
// The "noopener" feature string covers most browsers, but Safari ignores it for
// `window.open`, so the returned handle's opener is nulled as well.
export function openInNewTab(url: string): void {
  const opened = window.open(url, "_blank", "noopener");
  if (opened) opened.opener = null;
}
