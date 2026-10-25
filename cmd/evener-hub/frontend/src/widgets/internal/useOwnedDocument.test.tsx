import { act } from "@testing-library/react";
import { useRef } from "react";
import { createRoot } from "react-dom/client";
import { expect, test } from "vitest";
import { appendPopout } from "./popoutTestUtils";
import { useOwnedDocument } from "./useOwnedDocument";

function Probe({ seen }: { seen: Document[] }) {
  const ref = useRef<HTMLDivElement>(null);
  seen.push(useOwnedDocument(ref));
  return <div ref={ref} />;
}

// Dockview moves existing DOM between windows without a React render. A widget
// that only read its ownerDocument while rendering would keep the old window's
// portal and listeners; the hook has to turn the move into a render.
test("the owned document follows a container moved into another document", async () => {
  const { frame, doc } = appendPopout();
  const seen: Document[] = [];
  // The React container is the node Dockview would move; React never removes
  // its own container from a parent, so moving it does not break cleanup.
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

// Dockview's move can start with a removal and finish with an insertion after
// a turn. The watched document sees only the removal, so a one-shot check at
// that moment still reads the old owner and the move is lost.
test("a detached node's later insertion into another document is followed", async () => {
  const { frame, doc } = appendPopout();
  const seen: Document[] = [];
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() => root.render(<Probe seen={seen} />));
    expect(seen.at(-1)).toBe(document);

    act(() => host.remove());
    await act(async () => {});
    expect(seen.at(-1)).toBe(document);

    act(() => {
      doc.body.appendChild(host);
    });
    // The hook re-checks a detached node on a short timer.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 40));
    });

    expect(seen.at(-1)).toBe(doc);
  } finally {
    act(() => root.unmount());
    frame.remove();
    host.remove();
  }
});
