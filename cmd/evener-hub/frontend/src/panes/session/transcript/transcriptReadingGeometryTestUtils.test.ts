import { expect, test } from "vitest";
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
