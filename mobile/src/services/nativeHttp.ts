/**
 * Binary-safe authenticated Hub HTTP adapter.
 *
 * Metadata crosses JSON IPC under the Rust command's named `request` argument.
 * Request bytes use Tauri raw IPC, keyed only by an opaque request ID header.
 * Response metadata arrives on a separate Channel while the command resolves
 * with raw bytes. JSON is an explicit caller codec, never the transport.
 */

import type { TauriBridge } from "./tauri";

const ALLOWED_METHODS = new Set(["GET", "POST", "PUT", "PATCH"]);
const ALLOWED_PATH_PREFIXES = ["/api/", "/docs/", "/images/"];
const ALLOWED_MEDIA_TYPES = new Set([
  "application/json",
  "text/plain",
  "text/markdown",
  "image/jpeg",
  "image/png",
  "image/webp",
  "image/gif",
  "multipart/form-data",
]);

export const MAX_REQUEST_BODY_BYTES = 8 * 1024 * 1024;
export const MAX_RESPONSE_BODY_BYTES = 20 * 1024 * 1024;
export const REQUEST_ID_HEADER = "x-evener-request-id";

export interface HubHttpResponseHeaders {
  readonly contentType?: string;
  readonly contentLength?: string;
  readonly etag?: string;
  readonly lastModified?: string;
}

export interface HubHttpResponse {
  readonly status: number;
  readonly headers: HubHttpResponseHeaders;
  readonly mediaType: string | null;
  readonly body: Uint8Array;
}

export interface HubHttpRequestInput {
  readonly activeProfileId: string;
  readonly method: string;
  readonly path: string;
  readonly body?: Uint8Array | ArrayBuffer;
  readonly mediaType?: string;
  readonly signal?: AbortSignal;
}

export type HttpServiceErrorCode =
  | "validation_failed"
  | "cancelled"
  | "request_failed"
  | "decode_failed";

export class HttpServiceError extends Error {
  readonly code: HttpServiceErrorCode;
  constructor(code: HttpServiceErrorCode, message: string) {
    super(message);
    this.name = "HttpServiceError";
    this.code = code;
  }
}

export function isHttpServiceError(value: unknown): value is HttpServiceError {
  return value instanceof HttpServiceError;
}

export function encodeJsonBody(value: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(value));
}

export function decodeJsonBody<T = unknown>(body: Uint8Array): T {
  return JSON.parse(
    new TextDecoder("utf-8", { fatal: true }).decode(body),
  ) as T;
}

export function decodeTextBody(body: Uint8Array): string {
  return new TextDecoder("utf-8", { fatal: true }).decode(body);
}

function validationFailure(message: string): never {
  throw new HttpServiceError("validation_failed", message);
}

function validateMethod(method: string): void {
  if (!ALLOWED_METHODS.has(method)) validationFailure("method not allowed");
}

/** Mirrors Rust `validate_path`: split query first, strict one-pass percent
 * decoding, reject remaining percent signs and decoded separators/dot segments. */
export function validateHttpPath(input: string): void {
  if (typeof input !== "string") validationFailure("path not allowed");
  const queryAt = input.indexOf("?");
  const path = queryAt < 0 ? input : input.slice(0, queryAt);
  if (
    path.length === 0 ||
    !path.startsWith("/") ||
    path.startsWith("//") ||
    path.includes("\\")
  ) {
    validationFailure("path not allowed");
  }
  let decoded: string;
  try {
    decoded = decodeURIComponent(path);
  } catch {
    validationFailure("path not allowed");
  }
  if (
    decoded.includes("%") ||
    decoded.startsWith("//") ||
    decoded.includes("//") ||
    decoded.includes("\\") ||
    decoded.split("/").some((segment) => segment === "." || segment === "..") ||
    !ALLOWED_PATH_PREFIXES.some((prefix) => decoded.startsWith(prefix))
  ) {
    validationFailure("path not allowed");
  }
}

function validateMediaType(mediaType: string | undefined): void {
  if (mediaType === undefined) return;
  if (mediaType.includes("\r") || mediaType.includes("\n")) {
    validationFailure("media type not allowed");
  }
  const base = mediaType.split(";", 1)[0]?.trim() ?? "";
  if (!ALLOWED_MEDIA_TYPES.has(base))
    validationFailure("media type not allowed");
}

function requestBytes(body: Uint8Array | ArrayBuffer | undefined): Uint8Array {
  if (body === undefined) return new Uint8Array();
  const bytes = ArrayBuffer.isView(body)
    ? new Uint8Array(body.buffer, body.byteOffset, body.byteLength)
    : new Uint8Array(body);
  if (bytes.byteLength > MAX_REQUEST_BODY_BYTES) {
    validationFailure("body exceeds maximum size");
  }
  return bytes;
}

function record(value: unknown, message: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new HttpServiceError("decode_failed", message);
  }
  return value as Record<string, unknown>;
}

function exactKeys(
  value: Record<string, unknown>,
  required: readonly string[],
  optional: readonly string[] = [],
): void {
  for (const key of required) {
    if (!(key in value)) {
      throw new HttpServiceError("decode_failed", "invalid response metadata");
    }
  }
  for (const key of Object.keys(value)) {
    if (!required.includes(key) && !optional.includes(key)) {
      throw new HttpServiceError(
        "decode_failed",
        `unknown field "${key}" in http response`,
      );
    }
  }
}

interface ResponseMetadata {
  readonly requestId: string;
  readonly status: number;
  readonly headers: HubHttpResponseHeaders;
  readonly mediaType: string | null;
  readonly bodyLength: number;
}

function decodeHeaders(value: unknown): HubHttpResponseHeaders {
  const headers = record(value, "invalid response headers");
  exactKeys(
    headers,
    [],
    ["contentType", "contentLength", "etag", "lastModified"],
  );
  for (const [key, item] of Object.entries(headers)) {
    if (typeof item !== "string") {
      throw new HttpServiceError(
        "decode_failed",
        `field "${key}" must be a string`,
      );
    }
  }
  return headers as HubHttpResponseHeaders;
}

function decodeMetadata(value: unknown, requestId: string): ResponseMetadata {
  const metadata = record(value, "invalid response metadata");
  exactKeys(metadata, [
    "requestId",
    "status",
    "headers",
    "mediaType",
    "bodyLength",
  ]);
  if (metadata.requestId !== requestId) {
    throw new HttpServiceError("decode_failed", "response request mismatch");
  }
  if (
    typeof metadata.status !== "number" ||
    !Number.isInteger(metadata.status) ||
    metadata.status < 100 ||
    metadata.status > 599
  ) {
    throw new HttpServiceError("decode_failed", "invalid response status");
  }
  if (metadata.mediaType !== null && typeof metadata.mediaType !== "string") {
    throw new HttpServiceError("decode_failed", "invalid response media type");
  }
  if (
    typeof metadata.bodyLength !== "number" ||
    !Number.isSafeInteger(metadata.bodyLength) ||
    metadata.bodyLength < 0 ||
    metadata.bodyLength > MAX_RESPONSE_BODY_BYTES
  ) {
    throw new HttpServiceError("decode_failed", "invalid response body length");
  }
  return {
    requestId,
    status: metadata.status,
    headers: decodeHeaders(metadata.headers),
    mediaType: metadata.mediaType,
    bodyLength: metadata.bodyLength,
  };
}

function decodeRawBody(value: unknown): Uint8Array {
  if (
    ArrayBuffer.isView(value) &&
    Object.prototype.toString.call(value) === "[object Uint8Array]"
  ) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  throw new HttpServiceError("decode_failed", "expected a raw response body");
}

export interface HttpService {
  request(input: HubHttpRequestInput): Promise<HubHttpResponse>;
}

interface HttpServiceOptions {
  readonly newRequestId?: () => string;
}

function defaultRequestId(): string {
  return crypto.randomUUID().replaceAll("-", "_");
}

export function createHttpService(
  bridge: TauriBridge,
  options: HttpServiceOptions = {},
): HttpService {
  const newRequestId = options.newRequestId ?? defaultRequestId;
  return {
    async request(input) {
      if (input.signal?.aborted) {
        throw new HttpServiceError("cancelled", "request cancelled");
      }
      validateMethod(input.method);
      validateHttpPath(input.path);
      validateMediaType(input.mediaType);
      const body = requestBytes(input.body);
      const requestId = newRequestId();

      let cancelStarted = false;
      let cancelSettlement: Promise<void> | undefined;
      const cancelOnce = () => {
        if (cancelStarted) return;
        cancelStarted = true;
        cancelSettlement = bridge
          .invoke("hub_http_cancel", { request: { requestId } })
          .then(() => undefined)
          .catch(() => undefined);
      };
      const onAbort = () => cancelOnce();
      input.signal?.addEventListener("abort", onAbort, { once: true });

      try {
        await bridge.invoke("hub_http_prepare", {
          request: {
            requestId,
            activeProfileId: input.activeProfileId,
            method: input.method,
            path: input.path,
            bodyLength: body.byteLength,
            mediaType: input.mediaType ?? null,
          },
        });
        if (input.signal?.aborted) cancelOnce();
        if (!cancelStarted && body.byteLength > 0) {
          await bridge.invoke("hub_http_upload_body", body, {
            headers: { [REQUEST_ID_HEADER]: requestId },
          });
        }
        if (input.signal?.aborted) cancelOnce();

        let settleMetadata: ((metadata: ResponseMetadata) => void) | undefined;
        let rejectMetadata: ((cause: unknown) => void) | undefined;
        const metadataPromise = new Promise<ResponseMetadata>(
          (resolve, reject) => {
            settleMetadata = resolve;
            rejectMetadata = reject;
          },
        );
        const channel = bridge.createChannel<unknown>((event) => {
          try {
            settleMetadata?.(decodeMetadata(event, requestId));
          } catch (cause) {
            rejectMetadata?.(cause);
          }
        });
        let raw: unknown;
        if (!cancelStarted) {
          raw = await bridge.invoke("hub_http_request", {
            request: { requestId },
            onResponse: channel,
          });
        }
        if (cancelSettlement) await cancelSettlement;
        if (input.signal?.aborted || cancelStarted) {
          throw new HttpServiceError("cancelled", "request cancelled");
        }
        const metadata = await metadataPromise;
        const responseBody = decodeRawBody(raw);
        if (responseBody.byteLength !== metadata.bodyLength) {
          throw new HttpServiceError(
            "decode_failed",
            "response body length mismatch",
          );
        }
        return {
          status: metadata.status,
          headers: metadata.headers,
          mediaType: metadata.mediaType,
          body: responseBody,
        };
      } catch (cause) {
        if (input.signal?.aborted || cancelStarted) {
          cancelOnce();
          if (cancelSettlement) await cancelSettlement;
          throw new HttpServiceError("cancelled", "request cancelled");
        }
        if (isHttpServiceError(cause)) throw cause;
        void cause;
        throw new HttpServiceError("request_failed", "hub request failed");
      } finally {
        input.signal?.removeEventListener("abort", onAbort);
      }
    },
  };
}
