import { act, render } from "@testing-library/react";
import { useRef } from "react";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import { expect, test } from "vitest";
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
