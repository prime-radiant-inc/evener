// Opening a URL in a new tab must not leave the new document with an opener:
// an opener-created window copies this tab's sessionStorage - where the
// per-client mutation identity lives (stores/mutationClientIdentity.ts) - and a
// shared identity would let both tabs claim each other's durable sends. That
// copy happens as the window is created, so nothing done to the handle
// afterwards can take it back: a browser that ignores `window.open`'s
// "noopener" feature string (Safari does) has already copied the storage by the
// time the handle comes back, and nulling its opener only hides the channel.
// An anchor carrying `rel="noopener noreferrer"` is honored by every browser
// the app runs in - it is the policy the app's other new-tab links already
// carry - so the open goes through one.
//
// The URL may be server-provided (the OAuth caller passes `login.url`), so the
// scheme is checked before anything is clicked: a `javascript:` href would run
// in this origin the moment the anchor is clicked, and `rel="noopener
// noreferrer"` governs the opener relationship, not what the URL does. Only
// http(s) is opened; anything else is refused loudly so each caller's existing
// error handling reports it instead of opening a hostile URL.
export function openInNewTab(url: string): void {
  // Resolved against this document so the command palette's root-relative
  // session path still opens - its resolved scheme is this origin's - while an
  // absolute server URL is inspected as written.
  let resolved: URL;
  try {
    resolved = new URL(url, window.location.href);
  } catch {
    throw new Error(`refusing to open an unparseable URL: ${url}`);
  }
  if (resolved.protocol !== "https:" && resolved.protocol !== "http:") {
    throw new Error(`refusing to open a URL with the ${resolved.protocol} scheme`);
  }
  const anchor = document.createElement("a");
  anchor.setAttribute("href", url);
  anchor.target = "_blank";
  anchor.rel = "noopener noreferrer";
  // Hidden, and attached for the click: a detached element's click is not a
  // navigation in every browser. Both are undone as soon as the click returns,
  // so the anchor never reaches a layout the user can see.
  anchor.style.display = "none";
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
}
