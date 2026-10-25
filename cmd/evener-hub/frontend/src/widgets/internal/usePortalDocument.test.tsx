import { act, render } from "@testing-library/react";
import { useRef } from "react";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import { expect, test, vi } from "vitest";
import { appendPopout } from "./popoutTestUtils";
import { usePortalDocument } from "./usePortalDocument";

function Probe({ seen }: { seen: (Document | null)[] }) {
  const ref = useRef<HTMLSpanElement>(null);
  seen.push(usePortalDocument(ref));
  return <span ref={ref} />;
}

// A portal-only widget has no node on its first render. The seam must answer
// null then, and its first non-null answer must already be the document the
// marker was mounted into - never the opener it briefly resolves through.
test("a marker mounted in a popout resolves that document before the panel renders", () => {
  const { frame, doc } = appendPopout();
  const seen: (Document | null)[] = [];
  const view = render(createPortal(<Probe seen={seen} />, doc.body));
  try {
    const resolved = seen.filter((entry): entry is Document => entry !== null);
    expect(resolved.length).toBeGreaterThan(0);
    expect(resolved[0]).toBe(doc);
    expect(seen[0]).toBeNull();
  } finally {
    view.unmount();
    frame.remove();
  }
});

test("a marker in the opener resolves the opener document", () => {
  const seen: (Document | null)[] = [];
  const view = render(<Probe seen={seen} />);
  try {
    expect(seen.filter((entry): entry is Document => entry !== null)[0]).toBe(document);
  } finally {
    view.unmount();
  }
});

test("the resolved document follows a move while mounted", async () => {
  const { frame, doc } = appendPopout();
  const seen: (Document | null)[] = [];
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() => root.render(<Probe seen={seen} />));
    expect(seen.at(-1)).toBe(document);
    act(() => {
      doc.body.appendChild(host);
    });
    await act(async () => {});
    expect(seen.at(-1)).toBe(doc);
  } finally {
    act(() => root.unmount());
    frame.remove();
    host.remove();
  }
});

function DeferredProbe({ seen, attach }: { seen: (Document | null)[]; attach: boolean }) {
  const ref = useRef<HTMLSpanElement>(null);
  seen.push(usePortalDocument(ref));
  return attach ? <span ref={ref} /> : null;
}

// The marker is allowed to arrive after the first commit, and the seam's
// contract is that it answers null only until the marker has attached.
test("a marker that attaches after the first commit resolves its document", () => {
  const seen: (Document | null)[] = [];
  const view = render(<DeferredProbe seen={seen} attach={false} />);
  try {
    expect(seen.at(-1)).toBeNull();
    view.rerender(<DeferredProbe seen={seen} attach />);
    expect(seen.at(-1)).toBe(document);
  } finally {
    view.unmount();
  }
});

function ToggleProbe({ seen, enabled }: { seen: (Document | null)[]; enabled: boolean }) {
  const ref = useRef<HTMLSpanElement>(null);
  seen.push(usePortalDocument(ref, enabled));
  return <span ref={ref} />;
}

// A closed popover keeps its trigger mounted but renders no panel, so it has
// nothing to follow. Watching its document anyway multiplies one idle
// subscriber per closed popover across the document.
test("a widget with observation off does not watch the document", () => {
  const { frame, doc } = appendPopout();
  const observe = vi.spyOn(MutationObserver.prototype, "observe");
  const seen: (Document | null)[] = [];
  const view = render(createPortal(<ToggleProbe seen={seen} enabled={false} />, doc.body));
  try {
    expect(observe).not.toHaveBeenCalled();
    view.rerender(createPortal(<ToggleProbe seen={seen} enabled />, doc.body));
    expect(observe.mock.calls[0]?.[0]).toBe(doc.documentElement);
  } finally {
    observe.mockRestore();
    view.unmount();
    frame.remove();
  }
});
