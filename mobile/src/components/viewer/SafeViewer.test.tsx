import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AttachmentRef } from "../../conversation/model";
import type {
  ViewerFetchResult,
  ViewerService,
} from "../../services/viewerService";
import { ViewerError } from "../../services/viewerService";
import { SafeViewer } from "./SafeViewer";

afterEach(() => {
  cleanup();
});

// --- helpers ----------------------------------------------------------------

const JPEG = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 0x4a, 0x46]);
const PNG = new Uint8Array([
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x0d, 0x0a,
]);

function attachment(overrides: Partial<AttachmentRef> = {}): AttachmentRef {
  return { id: "a1", src: "/images/test.jpg", ...overrides };
}

function fakeViewerService(
  impl: Pick<ViewerService, "fetchAttachment"> & Partial<ViewerService>,
): ViewerService {
  return {
    cancel: vi.fn(),
    ...impl,
  };
}

function resolvingService(result: ViewerFetchResult): ViewerService {
  return fakeViewerService({
    fetchAttachment: vi.fn().mockResolvedValue(result),
  });
}

function rejectingService(error: ViewerError): ViewerService {
  return fakeViewerService({
    fetchAttachment: vi.fn().mockRejectedValue(error),
  });
}

function makeImageResult(
  bytes = JPEG,
  contentType = "image/jpeg",
): ViewerFetchResult {
  return { bytes, contentType, kind: "image" };
}

function makeTextResult(
  text: string,
  contentType: "text/plain" | "text/markdown" = "text/plain",
): ViewerFetchResult {
  return {
    bytes: new TextEncoder().encode(text),
    contentType,
    kind: "text",
  };
}

// --- loading state ----------------------------------------------------------

describe("SafeViewer — loading state", () => {
  it("shows a loading indicator while fetching", () => {
    const svc = fakeViewerService({
      fetchAttachment: vi.fn(() => new Promise<ViewerFetchResult>(() => {})),
    });
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("Loading…")).toBeDefined();
    expect(screen.getByRole("status")).toBeDefined();
  });

  it("renders a close button during loading", () => {
    const svc = fakeViewerService({
      fetchAttachment: vi.fn(() => new Promise<ViewerFetchResult>(() => {})),
    });
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("Close")).toBeDefined();
  });
});

// --- image rendering ---------------------------------------------------------

describe("SafeViewer — image rendering", () => {
  it("renders an image from the fetched bytes via object URL", async () => {
    const svc = resolvingService(makeImageResult());
    render(
      <SafeViewer
        attachment={attachment({ name: "photo.jpg" })}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    const img = await screen.findByRole("img");
    expect(img.getAttribute("src")).toMatch(/^blob:/);
    expect(img.getAttribute("alt")).toBe("photo.jpg");
  });

  it("uses attachment id as alt when name is absent", async () => {
    const svc = resolvingService(makeImageResult());
    render(
      <SafeViewer
        attachment={attachment({ name: undefined })}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    const img = await screen.findByRole("img");
    expect(img.getAttribute("alt")).toBe("a1");
  });

  it("renders PNG images", async () => {
    const svc = resolvingService({
      bytes: PNG,
      contentType: "image/png",
      kind: "image",
    });
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    const img = await screen.findByRole("img");
    expect(img.getAttribute("src")).toMatch(/^blob:/);
  });
});

// --- text rendering ----------------------------------------------------------

describe("SafeViewer — text rendering", () => {
  it("renders plain text content in a pre block", async () => {
    const svc = resolvingService(makeTextResult("hello world"));
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    const pre = await screen.findByText("hello world");
    expect(pre.tagName).toBe("PRE");
  });

  it("renders Markdown content as sanitized HTML", async () => {
    const svc = resolvingService(
      makeTextResult("**bold** text", "text/markdown"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    const strong = await screen.findByText("bold");
    expect(strong.tagName).toBe("STRONG");
  });

  it("strips script tags from Markdown content", async () => {
    const svc = resolvingService(
      makeTextResult("<script>alert(1)</script>\n\nhello", "text/markdown"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await waitFor(() => {
      expect(screen.queryByText("alert(1)")).toBeNull();
    });
    expect(screen.getByText("hello")).toBeDefined();
  });

  it("does not render images in Markdown content", async () => {
    const svc = resolvingService(
      makeTextResult("![alt](https://example.com/x.png)", "text/markdown"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await waitFor(() => {
      expect(document.querySelector("img")).toBeNull();
    });
  });
});

// --- external link handling --------------------------------------------------

describe("SafeViewer — external link handling", () => {
  it("invokes onExternalLink when a Markdown link is tapped", async () => {
    const onExternalLink = vi.fn();
    const svc = resolvingService(
      makeTextResult("[docs](https://example.com)", "text/markdown"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onExternalLink={onExternalLink}
        onClose={vi.fn()}
      />,
    );
    const link = await screen.findByRole("link");
    fireEvent.click(link);
    expect(onExternalLink).toHaveBeenCalledWith("https://example.com/");
  });

  it("prevents default navigation when onExternalLink is provided", async () => {
    const onExternalLink = vi.fn();
    const svc = resolvingService(
      makeTextResult("[docs](https://example.com)", "text/markdown"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onExternalLink={onExternalLink}
        onClose={vi.fn()}
      />,
    );
    const link = await screen.findByRole("link");
    const event = new MouseEvent("click", {
      bubbles: true,
      cancelable: true,
      composed: true,
    });
    fireEvent(link, event);
    expect(event.defaultPrevented).toBe(true);
  });

  it("does not invoke onExternalLink when tapping non-link Markdown content", async () => {
    const onExternalLink = vi.fn();
    const svc = resolvingService(makeTextResult("plain text", "text/markdown"));
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onExternalLink={onExternalLink}
        onClose={vi.fn()}
      />,
    );
    const content = await screen.findByText("plain text");
    fireEvent.click(content);
    expect(onExternalLink).not.toHaveBeenCalled();
  });
});

// --- unsupported format state ------------------------------------------------

describe("SafeViewer — unsupported format state", () => {
  it("shows an explicit unsupported-format row naming the format", async () => {
    const svc = rejectingService(
      new ViewerError("unsupported_format", "unsupported", "application/pdf"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("Unsupported format: application/pdf");
  });

  it("shows unsupported state for HTML content", async () => {
    const svc = rejectingService(
      new ViewerError("unsupported_format", "unsupported", "text/html"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("Unsupported format: text/html");
  });

  it("shows unsupported state for executable content", async () => {
    const svc = rejectingService(
      new ViewerError(
        "unsupported_format",
        "unsupported",
        "application/x-executable",
      ),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("Unsupported format: application/x-executable");
  });
});

// --- error states -----------------------------------------------------------

describe("SafeViewer — error states", () => {
  it("shows a fetch-failed error on fetch failure", async () => {
    const svc = rejectingService(new ViewerError("fetch_failed", "HTTP 500"));
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("Unable to load content");
    expect(screen.getByText("HTTP 500")).toBeDefined();
  });

  it("shows an oversized error", async () => {
    const svc = rejectingService(
      new ViewerError("oversized", "content exceeds limit"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("Unable to load content");
    expect(screen.getByText("content exceeds limit")).toBeDefined();
  });

  it("shows an invalid-UTF-8 error", async () => {
    const svc = rejectingService(
      new ViewerError("invalid_utf8", "not valid UTF-8"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("Unable to load content");
    expect(screen.getByText("not valid UTF-8")).toBeDefined();
  });
});

// --- cancelled state ---------------------------------------------------------

describe("SafeViewer — cancelled state", () => {
  it("shows a cancelled state when fetch is cancelled", async () => {
    const svc = rejectingService(
      new ViewerError("cancelled", "fetch cancelled"),
    );
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("Cancelled");
  });
});

// --- close handling ----------------------------------------------------------

describe("SafeViewer — close handling", () => {
  it("invokes onClose when the close button is tapped", async () => {
    const onClose = vi.fn();
    const svc = resolvingService(makeImageResult());
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={onClose}
      />,
    );
    await screen.findByRole("img");
    fireEvent.click(screen.getByText("Close"));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("calls viewerService.cancel when close is tapped", async () => {
    const cancel = vi.fn();
    const svc = fakeViewerService({
      fetchAttachment: vi.fn().mockResolvedValue(makeImageResult()),
      cancel,
    });
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByRole("img");
    fireEvent.click(screen.getByText("Close"));
    expect(cancel).toHaveBeenCalledWith("/images/test.jpg");
  });
});

// --- object URL revocation ---------------------------------------------------

describe("SafeViewer — object URL revocation", () => {
  it("revokes the object URL on unmount", async () => {
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL");
    const createSpy = vi.spyOn(URL, "createObjectURL");
    const svc = resolvingService(makeImageResult());
    const { unmount } = render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByRole("img");
    const objectUrl = createSpy.mock.calls[0]?.[0] as Blob;
    expect(objectUrl).toBeInstanceOf(Blob);
    unmount();
    expect(revokeSpy).toHaveBeenCalled();
    revokeSpy.mockRestore();
    createSpy.mockRestore();
  });

  it("revokes the object URL when close is tapped", async () => {
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL");
    const createSpy = vi.spyOn(URL, "createObjectURL");
    const svc = resolvingService(makeImageResult());
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByRole("img");
    fireEvent.click(screen.getByText("Close"));
    expect(revokeSpy).toHaveBeenCalled();
    revokeSpy.mockRestore();
    createSpy.mockRestore();
  });

  it("does not create an object URL for text content", async () => {
    const createSpy = vi.spyOn(URL, "createObjectURL");
    const svc = resolvingService(makeTextResult("hello"));
    render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("hello");
    expect(createSpy).not.toHaveBeenCalled();
    createSpy.mockRestore();
  });

  it("revokes the object URL when transitioning to an error state", async () => {
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL");
    // First fetch resolves with image, then we re-render with a failing service.
    let fetchImpl: () => Promise<ViewerFetchResult> = () =>
      Promise.resolve(makeImageResult());
    const svc = fakeViewerService({
      fetchAttachment: vi.fn(() => fetchImpl()),
    });
    const { rerender } = render(
      <SafeViewer
        attachment={attachment({ src: "/images/a.jpg" })}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByRole("img");
    // Change src to force a new fetch that rejects.
    fetchImpl = () => Promise.reject(new ViewerError("fetch_failed", "boom"));
    rerender(
      <SafeViewer
        attachment={attachment({ src: "/images/b.jpg" })}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByText("Unable to load content");
    // The image object URL should have been revoked during the transition.
    expect(revokeSpy).toHaveBeenCalled();
    revokeSpy.mockRestore();
  });
});

// --- service invocation ------------------------------------------------------

describe("SafeViewer — service invocation", () => {
  it("calls fetchAttachment with the attachment src and declared mediaType", async () => {
    const fetchAttachment = vi.fn().mockResolvedValue(makeImageResult());
    const svc = fakeViewerService({ fetchAttachment });
    render(
      <SafeViewer
        attachment={attachment({
          src: "/images/photo.png",
          mediaType: "image/png",
        })}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    await screen.findByRole("img");
    expect(fetchAttachment).toHaveBeenCalledWith(
      "/images/photo.png",
      "image/png",
    );
  });

  it("calls cancel on unmount", () => {
    const cancel = vi.fn();
    const svc = fakeViewerService({
      fetchAttachment: vi.fn(() => new Promise<ViewerFetchResult>(() => {})),
      cancel,
    });
    const { unmount } = render(
      <SafeViewer
        attachment={attachment()}
        viewerService={svc}
        onClose={vi.fn()}
      />,
    );
    unmount();
    expect(cancel).toHaveBeenCalledWith("/images/test.jpg");
  });
});
