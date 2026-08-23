import { describe, expect, it, vi } from "vitest";
import type { HttpService, HubHttpResponse } from "./nativeHttp";
import {
  createViewerService,
  isViewerError,
  MAX_IMAGE_BYTES,
  MAX_TEXT_BYTES,
  sniffFormat,
  type ViewerDiagnostic,
  type ViewerService,
  validateViewerBytes,
} from "./viewerService";

// --- helpers ----------------------------------------------------------------

const PROFILE = "11111111-1111-1111-1111-111111111111";

const JPEG = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 0x4a, 0x46]);
const PNG = new Uint8Array([
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x0d, 0x0a,
]);
const GIF = new Uint8Array([0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00]);
const WEBP = new Uint8Array([
  0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0, 0x57, 0x45, 0x42, 0x50,
]);
const PDF = new Uint8Array([0x25, 0x50, 0x44, 0x46, 0x2d, 0x31, 0x2e, 0x35]);
const ZIP = new Uint8Array([0x50, 0x4b, 0x03, 0x04, 0x14, 0, 0, 0]);
const ELF = new Uint8Array([0x7f, 0x45, 0x4c, 0x46, 0x02, 0x01, 0x01, 0]);
const MZ = new Uint8Array([0x4d, 0x5a, 0x90, 0, 0x03, 0, 0, 0]);
const HTML_BYTES = new TextEncoder().encode("<!DOCTYPE html>\n<html></html>");

function textBytes(s: string): Uint8Array {
  return new TextEncoder().encode(s);
}

function fakeHttp(response: Partial<HubHttpResponse>): HttpService {
  const base: HubHttpResponse = {
    status: 200,
    headers: {},
    mediaType: null,
    body: new Uint8Array(),
    ...response,
  };
  return {
    async request(): Promise<HubHttpResponse> {
      return base;
    },
  };
}

function fakeHttpWithSignal(
  response: Partial<HubHttpResponse>,
  onSignal?: (signal: AbortSignal) => void,
): HttpService {
  const base: HubHttpResponse = {
    status: 200,
    headers: {},
    mediaType: null,
    body: new Uint8Array(),
    ...response,
  };
  return {
    async request(input): Promise<HubHttpResponse> {
      if (input.signal) onSignal?.(input.signal);
      if (input.signal?.aborted) {
        throw new Error("aborted");
      }
      return base;
    },
  };
}

function service(
  http: HttpService,
  onDiagnostic?: (d: ViewerDiagnostic) => void,
): ViewerService {
  return createViewerService(http, PROFILE, { onDiagnostic });
}

// --- sniffFormat -----------------------------------------------------------

describe("sniffFormat", () => {
  it("detects JPEG magic bytes", () => {
    expect(sniffFormat(JPEG)).toBe("image/jpeg");
  });
  it("detects PNG magic bytes", () => {
    expect(sniffFormat(PNG)).toBe("image/png");
  });
  it("detects GIF magic bytes", () => {
    expect(sniffFormat(GIF)).toBe("image/gif");
  });
  it("detects WebP magic bytes (RIFF + WEBP at offset 8)", () => {
    expect(sniffFormat(WEBP)).toBe("image/webp");
  });
  it("rejects RIFF without WEBP tag", () => {
    const riffOnly = new Uint8Array([0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0]);
    expect(sniffFormat(riffOnly)).toBeNull();
  });
  it("detects PDF magic bytes", () => {
    expect(sniffFormat(PDF)).toBe("application/pdf");
  });
  it("detects ZIP magic bytes", () => {
    expect(sniffFormat(ZIP)).toBe("application/zip");
  });
  it("detects ELF executable", () => {
    expect(sniffFormat(ELF)).toBe("application/x-executable");
  });
  it("detects MZ/PE executable", () => {
    expect(sniffFormat(MZ)).toBe("application/x-msdos-program");
  });
  it("detects HTML from doctype", () => {
    expect(sniffFormat(HTML_BYTES)).toBe("text/html");
  });
  it("returns null for unrecognized bytes", () => {
    expect(sniffFormat(new Uint8Array([0, 1, 2, 3]))).toBeNull();
  });
  it("returns null for empty input", () => {
    expect(sniffFormat(new Uint8Array())).toBeNull();
  });
});

// --- validateViewerBytes: images --------------------------------------------

describe("validateViewerBytes — valid images", () => {
  it("accepts JPEG with matching content type", () => {
    const result = validateViewerBytes(JPEG, "image/jpeg", undefined);
    expect(result.contentType).toBe("image/jpeg");
    expect(result.kind).toBe("image");
  });
  it("accepts PNG with matching content type", () => {
    const result = validateViewerBytes(PNG, "image/png", undefined);
    expect(result.contentType).toBe("image/png");
    expect(result.kind).toBe("image");
  });
  it("accepts WebP with matching content type", () => {
    const result = validateViewerBytes(WEBP, "image/webp", undefined);
    expect(result.contentType).toBe("image/webp");
    expect(result.kind).toBe("image");
  });
  it("accepts GIF with matching content type", () => {
    const result = validateViewerBytes(GIF, "image/gif", undefined);
    expect(result.contentType).toBe("image/gif");
    expect(result.kind).toBe("image");
  });
  it("accepts image when content type is missing and magic bytes match", () => {
    const result = validateViewerBytes(JPEG, null, undefined);
    expect(result.contentType).toBe("image/jpeg");
    expect(result.kind).toBe("image");
  });
});

// --- validateViewerBytes: text -----------------------------------------------

describe("validateViewerBytes — valid text", () => {
  it("accepts plain text with valid UTF-8", () => {
    const bytes = textBytes("hello world");
    const result = validateViewerBytes(bytes, "text/plain", undefined);
    expect(result.contentType).toBe("text/plain");
    expect(result.kind).toBe("text");
  });
  it("accepts Markdown content type", () => {
    const bytes = textBytes("# heading\n\nbody");
    const result = validateViewerBytes(bytes, "text/markdown", undefined);
    expect(result.contentType).toBe("text/markdown");
    expect(result.kind).toBe("text");
  });
  it("accepts content type with charset parameter", () => {
    const bytes = textBytes("hello");
    const result = validateViewerBytes(
      bytes,
      "text/plain; charset=utf-8",
      undefined,
    );
    expect(result.contentType).toBe("text/plain");
  });
  it("accepts multibyte UTF-8 text", () => {
    const bytes = textBytes("日本語 テスト αβγ");
    const result = validateViewerBytes(bytes, "text/plain", undefined);
    expect(result.kind).toBe("text");
  });
});

// --- validateViewerBytes: MIME sniff mismatch --------------------------------

describe("validateViewerBytes — MIME sniff mismatch", () => {
  it("sniffs supported image type when content type missing", () => {
    const result = validateViewerBytes(PNG, null, undefined);
    expect(result.contentType).toBe("image/png");
  });
  it("uses sniffed supported type when declared image content does not match bytes", () => {
    // Content-Type says image/jpeg but bytes are PNG — sniffed type wins.
    const result = validateViewerBytes(PNG, "image/jpeg", undefined);
    expect(result.contentType).toBe("image/png");
  });
  it("emits a diagnostic when declared mediaType conflicts with resolved type", () => {
    const onDiagnostic = vi.fn();
    const bytes = textBytes("hello");
    validateViewerBytes(
      bytes,
      "text/plain",
      "image/jpeg",
      onDiagnostic,
      "https://x/y",
    );
    expect(onDiagnostic).toHaveBeenCalledWith({
      kind: "mime_mismatch",
      declared: "image/jpeg",
      actual: "text/plain",
      url: "https://x/y",
    });
  });
  it("does not emit a diagnostic when declared mediaType matches", () => {
    const onDiagnostic = vi.fn();
    validateViewerBytes(
      textBytes("hi"),
      "text/plain",
      "text/plain",
      onDiagnostic,
    );
    expect(onDiagnostic).not.toHaveBeenCalled();
  });
});

// --- validateViewerBytes: wrong MIME (image declared, actual PDF) -----------

describe("validateViewerBytes — wrong MIME type", () => {
  it("rejects image content type with PDF bytes", () => {
    expect(() => validateViewerBytes(PDF, "image/jpeg", undefined)).toThrow();
    try {
      validateViewerBytes(PDF, "image/jpeg", undefined);
    } catch (e) {
      expect(isViewerError(e)).toBe(true);
      if (isViewerError(e)) {
        expect(e.code).toBe("unsupported_format");
      }
    }
  });
  it("sniffs PDF bytes even with missing content type", () => {
    try {
      validateViewerBytes(PDF, null, undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("unsupported_format");
    }
  });
});

// --- validateViewerBytes: oversized ------------------------------------------

describe("validateViewerBytes — oversized bodies", () => {
  it("rejects images exceeding 20 MiB", () => {
    const bytes = new Uint8Array(MAX_IMAGE_BYTES + 1);
    bytes[0] = 0xff;
    bytes[1] = 0xd8;
    bytes[2] = 0xff;
    try {
      validateViewerBytes(bytes, "image/jpeg", undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("oversized");
    }
  });
  it("rejects text exceeding 2 MiB", () => {
    const bytes = new Uint8Array(MAX_TEXT_BYTES + 1);
    bytes.fill(0x61); // 'a'
    try {
      validateViewerBytes(bytes, "text/plain", undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("oversized");
    }
  });
  it("accepts image exactly at 20 MiB limit", () => {
    const bytes = new Uint8Array(MAX_IMAGE_BYTES);
    bytes[0] = 0xff;
    bytes[1] = 0xd8;
    bytes[2] = 0xff;
    const result = validateViewerBytes(bytes, "image/jpeg", undefined);
    expect(result.contentType).toBe("image/jpeg");
  });
  it("accepts text exactly at 2 MiB limit", () => {
    const bytes = new Uint8Array(MAX_TEXT_BYTES);
    bytes.fill(0x61);
    const result = validateViewerBytes(bytes, "text/plain", undefined);
    expect(result.contentType).toBe("text/plain");
  });
});

// --- validateViewerBytes: invalid UTF-8 --------------------------------------

describe("validateViewerBytes — invalid UTF-8", () => {
  it("rejects text content with invalid UTF-8 sequences", () => {
    // 0xff 0xfe is not a valid UTF-8 start.
    const bytes = new Uint8Array([0xff, 0xfe, 0x61, 0x62]);
    try {
      validateViewerBytes(bytes, "text/plain", undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("invalid_utf8");
    }
  });
  it("rejects text with lone continuation byte", () => {
    const bytes = new Uint8Array([0x61, 0x80, 0x62]);
    try {
      validateViewerBytes(bytes, "text/plain", undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("invalid_utf8");
    }
  });
  it("does not UTF-8 validate image content", () => {
    // JPEG bytes contain non-UTF-8 but should pass as image.
    const result = validateViewerBytes(JPEG, "image/jpeg", undefined);
    expect(result.kind).toBe("image");
  });
});

// --- validateViewerBytes: rejected content types -----------------------------

describe("validateViewerBytes — rejected formats", () => {
  it("rejects HTML content type", () => {
    try {
      validateViewerBytes(HTML_BYTES, "text/html", undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("unsupported_format");
      if (isViewerError(e)) expect(e.format).toBe("text/html");
    }
  });
  it("rejects PDF content type", () => {
    try {
      validateViewerBytes(PDF, "application/pdf", undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("unsupported_format");
      if (isViewerError(e)) expect(e.format).toBe("application/pdf");
    }
  });
  it("rejects zip archive content type", () => {
    try {
      validateViewerBytes(ZIP, "application/zip", undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("unsupported_format");
      if (isViewerError(e)) expect(e.format).toBe("application/zip");
    }
  });
  it("rejects application/octet-stream content type", () => {
    try {
      validateViewerBytes(
        new Uint8Array([0, 1, 2, 3]),
        "application/octet-stream",
        undefined,
      );
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("unsupported_format");
    }
  });
  it("rejects ELF executable via magic bytes", () => {
    try {
      validateViewerBytes(ELF, null, undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("unsupported_format");
      if (isViewerError(e)) expect(e.format).toBe("application/x-executable");
    }
  });
  it("rejects MZ/PE executable via magic bytes", () => {
    try {
      validateViewerBytes(MZ, null, undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("unsupported_format");
    }
  });
  it("rejects unknown binary content with missing content type", () => {
    try {
      validateViewerBytes(new Uint8Array([0, 1, 2, 3]), null, undefined);
      throw new Error("should have thrown");
    } catch (e) {
      expect(isViewerError(e) && e.code).toBe("unsupported_format");
    }
  });
});

// --- createViewerService: fetch + validation integration ---------------------

describe("createViewerService — HTTP fetch integration", () => {
  it("fetches and returns validated image bytes", async () => {
    const http = fakeHttp({
      status: 200,
      body: JPEG,
      headers: { contentType: "image/jpeg" },
      mediaType: "image/jpeg",
    });
    const result = await service(http).fetchAttachment("/images/test.jpg");
    expect(result.bytes).toEqual(JPEG);
    expect(result.contentType).toBe("image/jpeg");
    expect(result.kind).toBe("image");
  });

  it("fetches and returns validated text bytes", async () => {
    const bytes = textBytes("plain text content");
    const http = fakeHttp({
      status: 200,
      body: bytes,
      headers: { contentType: "text/plain" },
      mediaType: "text/plain",
    });
    const result = await service(http).fetchAttachment("/docs/readme.txt");
    expect(result.kind).toBe("text");
    expect(result.contentType).toBe("text/plain");
  });

  it("fetches Markdown bytes", async () => {
    const bytes = textBytes("# Title\n\ntext");
    const http = fakeHttp({
      status: 200,
      body: bytes,
      headers: { contentType: "text/markdown" },
      mediaType: "text/markdown",
    });
    const result = await service(http).fetchAttachment("/docs/readme.md");
    expect(result.kind).toBe("text");
    expect(result.contentType).toBe("text/markdown");
  });

  it("rejects non-2xx HTTP status", async () => {
    const http = fakeHttp({
      status: 404,
      body: textBytes("not found"),
      headers: { contentType: "text/plain" },
      mediaType: "text/plain",
    });
    await expect(
      service(http).fetchAttachment("/docs/missing.txt"),
    ).rejects.toMatchObject({ code: "fetch_failed" });
  });

  it("rejects HTML response", async () => {
    const http = fakeHttp({
      status: 200,
      body: HTML_BYTES,
      headers: { contentType: "text/html" },
      mediaType: "text/html",
    });
    await expect(
      service(http).fetchAttachment("/docs/page.html"),
    ).rejects.toMatchObject({ code: "unsupported_format" });
  });

  it("rejects PDF response", async () => {
    const http = fakeHttp({
      status: 200,
      body: PDF,
      headers: { contentType: "application/pdf" },
      mediaType: "application/pdf",
    });
    await expect(
      service(http).fetchAttachment("/docs/report.pdf"),
    ).rejects.toMatchObject({ code: "unsupported_format" });
  });

  it("rejects archive response", async () => {
    const http = fakeHttp({
      status: 200,
      body: ZIP,
      headers: { contentType: "application/zip" },
      mediaType: "application/zip",
    });
    await expect(
      service(http).fetchAttachment("/docs/archive.zip"),
    ).rejects.toMatchObject({ code: "unsupported_format" });
  });

  it("sniffs unsupported format when content type is missing", async () => {
    const http = fakeHttp({
      status: 200,
      body: PDF,
      headers: {},
      mediaType: null,
    });
    await expect(
      service(http).fetchAttachment("/docs/unknown"),
    ).rejects.toMatchObject({ code: "unsupported_format" });
  });

  it("emits MIME mismatch diagnostic when declared type conflicts", async () => {
    const onDiagnostic = vi.fn();
    const http = fakeHttp({
      status: 200,
      body: textBytes("hello"),
      headers: { contentType: "text/plain" },
      mediaType: "text/plain",
    });
    await service(http, onDiagnostic).fetchAttachment("/docs/x", "image/jpeg");
    expect(onDiagnostic).toHaveBeenCalledWith({
      kind: "mime_mismatch",
      declared: "image/jpeg",
      actual: "text/plain",
      url: "/docs/x",
    });
  });

  it("prefer response content type over declared mediaType", async () => {
    const http = fakeHttp({
      status: 200,
      body: PNG,
      headers: { contentType: "image/png" },
      mediaType: "image/png",
    });
    // declared says image/jpeg but response says image/png — response wins.
    const result = await service(http).fetchAttachment(
      "/images/x",
      "image/jpeg",
    );
    expect(result.contentType).toBe("image/png");
  });
});

// --- cancellation ------------------------------------------------------------

describe("createViewerService — cancellation", () => {
  it("cancel() aborts an in-flight fetch", async () => {
    let capturedSignal: AbortSignal | undefined;
    const http = fakeHttpWithSignal(
      {
        body: JPEG,
        headers: { contentType: "image/jpeg" },
        mediaType: "image/jpeg",
      },
      (signal) => {
        capturedSignal = signal;
      },
    );
    const svc = service(http);
    const url = "/images/cancel.jpg";
    const promise = svc.fetchAttachment(url);
    // Abort the signal immediately to simulate mid-flight cancel.
    expect(capturedSignal).toBeDefined();
    svc.cancel(url);
    await expect(promise).rejects.toMatchObject({ code: "cancelled" });
  });

  it("cancel() on a URL with no in-flight fetch is a no-op", () => {
    const http = fakeHttp({
      body: JPEG,
      headers: { contentType: "image/jpeg" },
    });
    expect(() => service(http).cancel("/images/none.jpg")).not.toThrow();
  });

  it("returns cancelled when the signal aborts before the response arrives", async () => {
    let resolveRequest!: (value: HubHttpResponse) => void;
    const pending = new Promise<HubHttpResponse>((resolve) => {
      resolveRequest = resolve;
    });
    const http: HttpService = {
      async request(input): Promise<HubHttpResponse> {
        // If already aborted, throw immediately.
        if (input.signal?.aborted) throw new Error("aborted");
        // Otherwise hang until resolved or aborted.
        return Promise.race([
          pending,
          new Promise<never>((_r, reject) => {
            input.signal?.addEventListener(
              "abort",
              () => reject(new Error("aborted")),
              { once: true },
            );
          }),
        ]);
      },
    };
    const svc = service(http);
    const url = "/images/hang.jpg";
    const promise = svc.fetchAttachment(url);
    // Cancel while the request is still pending.
    svc.cancel(url);
    resolveRequest({
      status: 200,
      headers: { contentType: "image/jpeg" },
      mediaType: "image/jpeg",
      body: JPEG,
    });
    await expect(promise).rejects.toMatchObject({ code: "cancelled" });
  });
});

// --- data: URLs --------------------------------------------------------------

describe("createViewerService — data: URLs", () => {
  it("decodes base64 data URL images", async () => {
    const b64 = btoa(String.fromCharCode(...JPEG));
    const url = `data:image/jpeg;base64,${b64}`;
    const result = await service(fakeHttp({})).fetchAttachment(
      url,
      "image/jpeg",
    );
    expect(result.contentType).toBe("image/jpeg");
    expect(result.kind).toBe("image");
    expect(result.bytes).toEqual(JPEG);
  });

  it("decodes plain text data URLs", async () => {
    const url = "data:text/plain,hello%20world";
    const result = await service(fakeHttp({})).fetchAttachment(url);
    expect(result.contentType).toBe("text/plain");
    expect(result.kind).toBe("text");
  });

  it("rejects HTML data URLs", async () => {
    const b64 = btoa(String.fromCharCode(...HTML_BYTES));
    const url = `data:text/html;base64,${b64}`;
    await expect(
      service(fakeHttp({})).fetchAttachment(url),
    ).rejects.toMatchObject({ code: "unsupported_format" });
  });
});
