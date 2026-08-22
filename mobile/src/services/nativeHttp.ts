/**
 * Authenticated Hub HTTP service — typed adapter over the Rust
 * `hub_http_request` Tauri command in `mobile/src-tauri/src/commands.rs`.
 *
 * The Rust layer holds the bearer token, pins the origin via NetworkPolicy,
 * disables redirects, and strips caller auth/cookie/hop headers (the DTO
 * carries no headers field). This module mirrors the request allowlist for
 * defense-in-depth and clearer client-side errors, decodes the redacted
 * response strictly, supports cancellation, and never echoes a secret in an
 * error.
 */

import type { TauriBridge } from "./tauri";

// ---------------------------------------------------------------------------
// Allowlists — mirror `http_transport.rs` (defense-in-depth + clear errors)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Typed request/response (mirror the Rust serde camelCase DTOs)
// ---------------------------------------------------------------------------

export interface HubHttpRequestArgs {
  readonly activeProfileId: string;
  readonly method: string;
  readonly path: string;
  readonly body: unknown;
  readonly mediaType: string | null;
}

export interface HubHttpResponse {
  readonly status: number;
  readonly body: unknown;
}

export interface HubHttpRequestInput {
  readonly activeProfileId: string;
  readonly method: string;
  readonly path: string;
  readonly body?: unknown;
  readonly mediaType?: string;
  /** Abort the in-flight request. */
  readonly signal?: AbortSignal;
}

// ---------------------------------------------------------------------------
// Structured error — never carries a secret
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Request validation (mirrors the Rust allowlist)
// ---------------------------------------------------------------------------

function validateMethod(method: string): void {
  if (!ALLOWED_METHODS.has(method)) {
    throw new HttpServiceError(
      "validation_failed",
      `method not allowed: ${method}`,
    );
  }
}

function validatePath(path: string): void {
  if (typeof path !== "string" || path.length === 0 || !path.startsWith("/")) {
    throw new HttpServiceError("validation_failed", "path not allowed");
  }
  // Reject dot segments (traversal) and double slashes, mirroring the Rust
  // validate_path: the backend re-validates, but failing early gives a clear
  // client error and never sends a traversal payload.
  if (path.includes("//") || path.includes("\\")) {
    throw new HttpServiceError("validation_failed", "path not allowed");
  }
  const segments = path.split("/");
  for (const seg of segments) {
    if (seg === "." || seg === "..") {
      throw new HttpServiceError("validation_failed", "path not allowed");
    }
  }
  if (!ALLOWED_PATH_PREFIXES.some((prefix) => path.startsWith(prefix))) {
    throw new HttpServiceError("validation_failed", "path not allowed");
  }
}

function validateMediaType(mediaType: string | undefined): void {
  if (mediaType === undefined) return;
  if (!ALLOWED_MEDIA_TYPES.has(mediaType)) {
    throw new HttpServiceError("validation_failed", "media type not allowed");
  }
}

// ---------------------------------------------------------------------------
// Response decoder — reject extra fields so a leaked secret fails closed
// ---------------------------------------------------------------------------

function decodeResponse(value: unknown): HubHttpResponse {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new HttpServiceError("decode_failed", "expected a response object");
  }
  const obj = value as Record<string, unknown>;
  for (const key of Object.keys(obj)) {
    if (key !== "status" && key !== "body") {
      throw new HttpServiceError(
        "decode_failed",
        `unknown field "${key}" in http response`,
      );
    }
  }
  if (typeof obj.status !== "number" || !Number.isFinite(obj.status)) {
    throw new HttpServiceError(
      "decode_failed",
      'field "status" must be a number',
    );
  }
  // body is a serde_json::Value: any JSON value is valid (object/array/string/
  // number/boolean/null). No further constraint.
  return { status: obj.status, body: obj.body };
}

// ---------------------------------------------------------------------------
// Service interface
// ---------------------------------------------------------------------------

export interface HttpService {
  request(input: HubHttpRequestInput): Promise<HubHttpResponse>;
}

export function createHttpService(bridge: TauriBridge): HttpService {
  return {
    async request(input) {
      // Cancel before invoking if already aborted.
      if (input.signal?.aborted) {
        throw new HttpServiceError("cancelled", "request cancelled");
      }

      // Validate (mirrors the Rust allowlist) before crossing the bridge.
      validateMethod(input.method);
      validatePath(input.path);
      validateMediaType(input.mediaType);

      const args: Record<string, unknown> = {
        activeProfileId: input.activeProfileId,
        method: input.method,
        path: input.path,
        body: input.body ?? null,
        mediaType: input.mediaType ?? null,
      };

      // Race the invoke against the abort signal so a late cancel still
      // rejects promptly with a typed cancellation error.
      const invokePromise = bridge.invoke<unknown>("hub_http_request", args);
      try {
        const result = await raceWithAbort(invokePromise, input.signal);
        return decodeResponse(result);
      } catch (cause) {
        if (isHttpServiceError(cause)) throw cause;
        if (isAbortError(input.signal, cause)) {
          throw new HttpServiceError("cancelled", "request cancelled");
        }
        // The Rust error string may carry a token/origin/path; never echo it.
        void cause;
        throw new HttpServiceError("request_failed", "hub request failed");
      }
    },
  };
}

function raceWithAbort<T>(
  promise: Promise<T>,
  signal?: AbortSignal,
): Promise<T> {
  if (signal === undefined) return promise;
  return new Promise<T>((resolve, reject) => {
    if (signal.aborted) {
      reject(new HttpServiceError("cancelled", "request cancelled"));
      return;
    }
    const onAbort = () => {
      reject(new HttpServiceError("cancelled", "request cancelled"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(
      (v) => {
        signal.removeEventListener("abort", onAbort);
        resolve(v);
      },
      (e) => {
        signal.removeEventListener("abort", onAbort);
        reject(e);
      },
    );
  });
}

function isAbortError(
  signal: AbortSignal | undefined,
  cause: unknown,
): boolean {
  if (signal?.aborted) return true;
  return cause instanceof Error && cause.name === "AbortError";
}
