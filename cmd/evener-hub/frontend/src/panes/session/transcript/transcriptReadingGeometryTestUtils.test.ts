import { expect, test } from "vitest";
import { nextRealTask } from "../../../stores/testing/stalledIndexedDB";
import { holdReaderFrames, installTranscriptGeometry } from "./transcriptReadingGeometryTestUtils";

// The fake's frame order is what reader tests rely on to match a browser's
// rendering update: scroll steps, then the frame's animation frame callbacks.
test("a held frame dispatches scroll events before its callbacks and runs a handler's request in that frame", () => {
  const section = document.createElement("section");
  section.dataset.testid = "transcript-virtual-list";
  const port = document.createElement("div");
  const sizer = document.createElement("div");
  sizer.style.height = "2000px";
  port.append(sizer);
  section.append(port);
  document.body.append(section);
  const geometry = installTranscriptGeometry(() => ({ width: 100, viewportHeight: 500, rowHeights: [] }));
  const frames = holdReaderFrames();
  const order: string[] = [];
  try {
    requestAnimationFrame(() => order.push(`earlier request sees ${port.scrollTop}`));
    port.addEventListener("scroll", () => {
      order.push(`scroll at ${port.scrollTop}`);
      requestAnimationFrame(() => order.push("handler request"));
    });
    port.scrollTop = 300;
    expect(order).toEqual([]);
    frames.release();
    expect(order).toEqual(["scroll at 300", "earlier request sees 300", "handler request"]);
  } finally {
    frames.restore();
    geometry.restore();
    section.remove();
  }
});

test("notify runs earlier-task frames after scroll steps and before ResizeObserver delivery", async () => {
  const port = document.createElement("div");
  const sizer = document.createElement("div");
  sizer.style.height = "2000px";
  port.append(sizer);
  document.body.append(port);
  const geometry = installTranscriptGeometry(
    () => ({ width: 100, viewportHeight: 500, rowHeights: [] }),
    () => port,
  );
  const frames = holdReaderFrames();
  const order: string[] = [];
  const observer = new ResizeObserver(() => order.push("resize"));
  observer.observe(port);
  try {
    requestAnimationFrame(() => order.push("earlier"));
    port.addEventListener("scroll", () => {
      order.push("scroll");
      requestAnimationFrame(() => order.push("handler"));
    });
    port.scrollTop = 300;
    // Yield to a new task, not a rendering update: the held requests must still precede notify.
    await nextRealTask();
    expect(order).toEqual([]);
    geometry.notify();
    expect(order).toEqual(["scroll", "earlier", "handler", "resize"]);
    frames.release();
    expect(order).toEqual(["scroll", "earlier", "handler", "resize"]);
  } finally {
    frames.restore();
    geometry.restore();
    port.remove();
  }
});

test("a frame callback can cancel a later callback in the captured batch", () => {
  const frames = holdReaderFrames();
  const order: number[] = [];
  try {
    requestAnimationFrame(() => {
      order.push(1);
      cancelAnimationFrame(later);
    });
    const later = requestAnimationFrame(() => order.push(2));
    frames.release();
    expect(order).toEqual([1]);
  } finally {
    frames.restore();
  }
});

test("filtered notify measures after the current batch and leaves callback-created work for the next update", () => {
  const port = document.createElement("div");
  const excluded = document.createElement("div");
  const sizer = document.createElement("div");
  sizer.style.height = "2000px";
  port.append(sizer);
  document.body.append(port, excluded);
  const sizes = { width: 100, viewportHeight: 500, rowHeights: [] };
  const geometry = installTranscriptGeometry(
    () => sizes,
    () => port,
  );
  const frames = holdReaderFrames();
  const order: string[] = [];
  const observer = new ResizeObserver((entries) => {
    expect(entries.map((entry) => entry.target)).toEqual([port]);
    order.push(`resize ${entries[0]?.contentRect.width}`);
    requestAnimationFrame(() => order.push("observer frame"));
  });
  observer.observe(port);
  observer.observe(excluded);
  try {
    port.addEventListener("scroll", () => order.push("scroll"));
    requestAnimationFrame(() => {
      order.push("earlier");
      sizes.width = 200;
      port.scrollTop = 300;
      requestAnimationFrame(() => order.push("nested frame"));
    });
    geometry.notify((target) => target === port);
    expect(order).toEqual(["earlier", "resize 200"]);
    frames.release();
    expect(order).toEqual(["earlier", "resize 200", "scroll", "nested frame", "observer frame"]);
    frames.release();
    expect(order).toEqual(["earlier", "resize 200", "scroll", "nested frame", "observer frame"]);
  } finally {
    frames.restore();
    geometry.restore();
    port.remove();
    excluded.remove();
  }
});

test("notify preserves pending scroll steps across nested holder restoration", () => {
  const port = document.createElement("div");
  const sizer = document.createElement("div");
  sizer.style.height = "2000px";
  port.append(sizer);
  document.body.append(port);
  const geometry = installTranscriptGeometry(
    () => ({ width: 100, viewportHeight: 500, rowHeights: [] }),
    () => port,
  );
  const outer = holdReaderFrames();
  const inner = holdReaderFrames();
  const order: string[] = [];
  const observer = new ResizeObserver(() => order.push("resize"));
  observer.observe(port);
  try {
    port.addEventListener("scroll", () => {
      order.push(`scroll ${port.scrollTop}`);
      requestAnimationFrame(() => order.push("handler"));
      if (port.scrollTop === 300) port.scrollTop = 400;
    });
    port.scrollTop = 300;
    inner.restore();
    requestAnimationFrame(() => order.push("outer"));
    geometry.notify();
    expect(order).toEqual(["scroll 300", "outer", "handler", "resize"]);
    geometry.notify();
    expect(order).toEqual(["scroll 300", "outer", "handler", "resize", "scroll 400", "handler", "resize"]);
    port.scrollTop = 500;
    geometry.notify();
    expect(order).toEqual([
      "scroll 300",
      "outer",
      "handler",
      "resize",
      "scroll 400",
      "handler",
      "resize",
      "scroll 500",
      "handler",
      "resize",
    ]);
  } finally {
    inner.restore();
    outer.restore();
    geometry.restore();
    port.remove();
  }
});

test("notify uses the installed holder and stops using it after restore", () => {
  const port = document.createElement("div");
  document.body.append(port);
  const geometry = installTranscriptGeometry(
    () => ({ width: 100, viewportHeight: 500, rowHeights: [] }),
    () => port,
  );
  const outer = holdReaderFrames();
  const inner = holdReaderFrames();
  const order: string[] = [];
  const observer = new ResizeObserver(() => order.push("resize"));
  observer.observe(port);
  try {
    requestAnimationFrame(() => order.push("inner"));
    geometry.notify();
    expect(order).toEqual(["inner", "resize"]);
    requestAnimationFrame(() => order.push("retired inner"));
    inner.restore();
    requestAnimationFrame(() => order.push("outer"));
    geometry.notify();
    expect(order).toEqual(["inner", "resize", "outer", "resize"]);
    requestAnimationFrame(() => order.push("retired outer"));
    outer.restore();
    geometry.notify();
    expect(order).toEqual(["inner", "resize", "outer", "resize", "resize"]);
  } finally {
    inner.restore();
    outer.restore();
    geometry.restore();
    port.remove();
  }
});
