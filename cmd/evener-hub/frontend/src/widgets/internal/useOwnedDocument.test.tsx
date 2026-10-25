import { act, render } from "@testing-library/react";
import { useRef } from "react";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import { expect, test, vi } from "vitest";
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

// The grace period belongs to the detachment, not to the mount: a menu open
// for longer than the window must still be followed when it is moved.
test("a long-lived node's later move is followed after a detached turn", async () => {
  vi.useFakeTimers();
  const { frame, doc } = appendPopout();
  const seen: Document[] = [];
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() => root.render(<Probe seen={seen} />));
    act(() => {
      vi.advanceTimersByTime(3000);
    });

    act(() => host.remove());
    await Promise.resolve();
    act(() => {
      doc.body.appendChild(host);
    });
    await act(async () => {
      vi.advanceTimersByTime(40);
    });

    expect(seen.at(-1)).toBe(doc);
  } finally {
    act(() => root.unmount());
    frame.remove();
    host.remove();
    vi.useRealTimers();
  }
});

// While a node is detached, document activity keeps the recheck chain alive.
// More than one chain would be untracked and outlive cleanup.
test("a detached node keeps one pending recheck, and unmount clears it", async () => {
  vi.useFakeTimers();
  const { frame } = appendPopout();
  const seen: Document[] = [];
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    act(() => root.render(<Probe seen={seen} />));
    act(() => host.remove());
    await act(async () => {});
    expect(vi.getTimerCount()).toBe(1);

    for (let index = 0; index < 3; index += 1) {
      act(() => {
        const noise = document.createElement("div");
        document.body.append(noise);
        noise.remove();
      });
      await act(async () => {});
    }
    expect(vi.getTimerCount()).toBe(1);

    act(() => root.unmount());
    expect(vi.getTimerCount()).toBe(0);
  } finally {
    frame.remove();
    host.remove();
    vi.useRealTimers();
  }
});

// A widget mounted directly in a popout renders before its ref exists, so the
// hook starts on the opener; the post-commit read has to correct it even
// though nothing else rendered.
test("a widget mounted directly in another document resolves it after mount", async () => {
  const { frame, doc } = appendPopout();
  const seen: Document[] = [];
  const view = render(createPortal(<Probe seen={seen} />, doc.body));
  try {
    await act(async () => {});
    expect(seen.at(-1)).toBe(doc);
  } finally {
    view.unmount();
    frame.remove();
  }
});
