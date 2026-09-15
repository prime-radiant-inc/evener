import { vi } from "vitest";

// Everything a caller of captureNewTabs needs to assert about a new tab: the
// URL it was opened at and the opener policy it was opened with.
const NEW_TAB_POLICY = "noopener noreferrer";

export { NEW_TAB_POLICY };

/** Records the new-tab opens `openInNewTab` performs, by intercepting the click
 * it makes on the anchor it built.
 *
 * The click is the one point every open passes through, and jsdom implements no
 * navigation, so intercepting it is what lets a test observe an open without a
 * browser. The anchors are returned in the order they were clicked; each one is
 * detached by the time the call returns, so the element itself is the only
 * record of the URL and policy it carried - hence `hrefAttribute` rather than
 * `href`, which would resolve against jsdom's document URL.
 */
export function captureNewTabs(): HTMLAnchorElement[] {
  const anchors: HTMLAnchorElement[] = [];
  // mockClear, so a file whose earlier test left the spy installed starts from
  // an empty record rather than inheriting its calls.
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockClear();
  click.mockImplementation(function (this: HTMLAnchorElement) {
    anchors.push(this);
  });
  return anchors;
}

/** The URL and opener policy of the one new tab a test expects, failing the
 * assertion itself rather than the property read when none was opened. */
export function openedNewTab(anchors: HTMLAnchorElement[]): { url: string | null; target: string; rel: string } {
  const [anchor] = anchors;
  if (anchors.length !== 1 || anchor === undefined) {
    throw new Error(`expected 1 new tab, got ${anchors.length}`);
  }
  return {
    url: anchor.getAttribute("href"),
    target: anchor.target,
    rel: anchor.rel,
  };
}
