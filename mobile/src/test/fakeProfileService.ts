/**
 * Fake {@link ProfileService} for store and screen tests.
 *
 * Mirrors the redacted surface of the real service: no token, raw QR text, or
 * capability is ever held or returned. Profiles are keyed by id and store only
 * `{id,name,origin}`. Failure injection is per-operation so tests can prove the
 * atomic "old profile stays usable" contract on re-pair/remove failure.
 */
import type {
  ConfirmPairingInput,
  HealthSnapshot,
  PreviewRepairInput,
  ProfilePreview,
  ProfileRedacted,
  ProfileService,
  RenameInput,
  SelectResult,
} from "../services/nativeProfiles";

export interface FakeProfileServiceSeed {
  readonly profiles?: readonly ProfileRedacted[];
  readonly activeProfileId?: string | null;
  readonly generation?: number;
}

export type FakeProfileOp =
  | "previewPaste"
  | "previewRepair"
  | "confirmPairing"
  | "rename"
  | "remove"
  | "select"
  | "health"
  | "list";

export class FakeProfileService implements ProfileService {
  private readonly profiles = new Map<string, ProfileRedacted>();
  private activeProfileId: string | null;
  private generation: number;
  private readonly previews = new Map<string, string>();
  private readonly repairProfileIds = new Map<string, string>();
  private previewCounter = 0;
  private readonly failQueue: FakeProfileOp[] = [];
  /**
   * Gated preview plumbing. {@link previewGateArmed} tracks whether the next
   * matching call should block; {@link pendingPreview} is the resolver captured
   * when the blocked call starts awaiting, so the test can resolve or reject
   * the in-flight call AFTER a superseding op or cancellation — proving the
   * stale result never publishes.
   */
  private previewGateArmed: {
    readonly op: "previewPaste" | "previewRepair";
  } | null = null;
  private pendingPreview: {
    readonly resolve: (value: ProfilePreview) => void;
    readonly reject: (cause: unknown) => void;
  } | null = null;
  private cancelGateArmed = false;
  private pendingCancelResolve: (() => void) | null = null;

  constructor(seed: FakeProfileServiceSeed = {}) {
    this.activeProfileId = seed.activeProfileId ?? null;
    this.generation = seed.generation ?? 0;
    for (const p of seed.profiles ?? []) {
      this.profiles.set(p.id, { ...p });
    }
  }

  /** Inject a one-shot failure for the next call of `op`. */
  failOnce(op: FakeProfileOp): void {
    this.failQueue.push(op);
  }

  /**
   * Arm a gate so the next `previewPaste` or `previewRepair` call blocks until
   * {@link resolvePreviewGate} / {@link rejectPreviewGate} is called. The test
   * resolves the in-flight call after arranging a superseding op or
   * cancellation, then observes that the stale result never published.
   */
  gatePreview(op: "previewPaste" | "previewRepair" = "previewPaste"): void {
    if (this.previewGateArmed !== null) {
      throw new Error("preview gate already armed");
    }
    this.previewGateArmed = { op };
  }

  /** Resolve the in-flight gated preview call; it returns `result`. */
  resolvePreviewGate(result: ProfilePreview): void {
    const pending = this.pendingPreview;
    if (pending === null) throw new Error("no preview gate pending");
    this.pendingPreview = null;
    pending.resolve(result);
  }

  /** Reject the in-flight gated preview call. */
  rejectPreviewGate(cause: unknown): void {
    const pending = this.pendingPreview;
    if (pending === null) throw new Error("no preview gate pending");
    this.pendingPreview = null;
    pending.reject(cause);
  }

  /** True if a gated preview call is in flight (blocked, not yet resolved). */
  isPreviewGatePending(): boolean {
    return this.pendingPreview !== null;
  }

  /** Block the next cancel call at the external service boundary. */
  gateCancelPreview(): void {
    if (this.cancelGateArmed) {
      throw new Error("cancel gate already armed");
    }
    this.cancelGateArmed = true;
  }

  /** Resolve the in-flight gated cancel call. */
  resolveCancelPreviewGate(): void {
    const resolve = this.pendingCancelResolve;
    if (resolve === null) throw new Error("no cancel gate pending");
    this.pendingCancelResolve = null;
    resolve();
  }

  /** True if a cancel call is blocked at the service boundary. */
  isCancelPreviewGatePending(): boolean {
    return this.pendingCancelResolve !== null;
  }

  /**
   * Gated health plumbing. When armed, the next `health()` call blocks until
   * {@link resolveHealthGate} is called. Lets a test prove that Onboarding
   * establishes the store generation synchronously at user intent, before
   * refresh resolves — so a late refresh cannot reverse click order or
   * launch a preview after unmount cancellation.
   */
  private healthGateArmed = false;
  private pendingHealthResolve: ((value: HealthSnapshot) => void) | null = null;

  /** Arm a gate so the next `health()` call blocks until resolved. */
  gateHealth(): void {
    if (this.healthGateArmed) {
      throw new Error("health gate already armed");
    }
    this.healthGateArmed = true;
  }

  /** Resolve the in-flight gated health call. */
  resolveHealthGate(): void {
    const resolve = this.pendingHealthResolve;
    if (resolve === null) throw new Error("no health gate pending");
    this.pendingHealthResolve = null;
    this.healthGateArmed = false;
    resolve({
      activeProfileId: this.activeProfileId,
      profiles: [...this.profiles.values()],
      generation: this.generation,
    });
  }

  /** True if a gated health call is in flight (blocked). */
  isHealthGatePending(): boolean {
    return this.pendingHealthResolve !== null;
  }

  private awaitGatedPreview(): Promise<ProfilePreview> {
    return new Promise<ProfilePreview>((resolve, reject) => {
      this.pendingPreview = { resolve, reject };
    });
  }

  private checkFail(op: FakeProfileOp): void {
    const idx = this.failQueue.indexOf(op);
    if (idx >= 0) {
      this.failQueue.splice(idx, 1);
      throw new Error(`${op} failed (redacted)`);
    }
  }

  // -- ProfileService --------------------------------------------------------

  async list(): Promise<ProfileRedacted[]> {
    return [...this.profiles.values()];
  }

  async previewPaste(input: { readonly raw: string }): Promise<ProfilePreview> {
    this.previewPasteCallCount += 1;
    this.checkFail("previewPaste");
    if (this.previewGateArmed?.op === "previewPaste") {
      this.previewGateArmed = null;
      const result = await this.awaitGatedPreview();
      this.previews.set(result.previewId, result.origin);
      this.previewIdsIssued.push(result.previewId);
      return result;
    }
    const previewId = `pv-${++this.previewCounter}`;
    // Parse a minimal http(s)://host[:port]/auth?token=... to extract origin.
    const origin = parseOrigin(input.raw);
    this.previews.set(previewId, origin);
    this.previewIdsIssued.push(previewId);
    return { previewId, origin };
  }

  async previewRepair(input: PreviewRepairInput): Promise<ProfilePreview> {
    this.checkFail("previewRepair");
    if (this.previewGateArmed?.op === "previewRepair") {
      this.previewGateArmed = null;
      const result = await this.awaitGatedPreview();
      this.previews.set(result.previewId, result.origin);
      this.repairProfileIds.set(result.previewId, input.profileId);
      this.previewIdsIssued.push(result.previewId);
      return result;
    }
    const previewId = `pv-${++this.previewCounter}`;
    const origin = parseOrigin(input.raw);
    this.previews.set(previewId, origin);
    this.repairProfileIds.set(previewId, input.profileId);
    this.previewIdsIssued.push(previewId);
    return { previewId, origin };
  }

  async confirmPairing(input: ConfirmPairingInput): Promise<ProfileRedacted> {
    this.checkFail("confirmPairing");
    const origin = this.previews.get(input.previewId);
    if (origin === undefined) {
      throw new Error("preview not found (redacted)");
    }
    const repairId = this.repairProfileIds.get(input.previewId);
    const id = repairId ?? `p-${this.profiles.size + 1}`;
    const profile: ProfileRedacted = { id, name: input.name, origin };
    this.profiles.set(id, profile);
    this.activeProfileId = id;
    this.generation += 1;
    this.previews.delete(input.previewId);
    this.repairProfileIds.delete(input.previewId);
    return profile;
  }

  async cancelPreview(_input: { readonly previewId: string }): Promise<void> {
    this.cancelPreviewCalls.push(_input.previewId);
    if (this.cancelGateArmed) {
      this.cancelGateArmed = false;
      await new Promise<void>((resolve) => {
        this.pendingCancelResolve = resolve;
      });
    }
    this.previews.delete(_input.previewId);
    this.repairProfileIds.delete(_input.previewId);
  }

  async clearPreviews(): Promise<void> {
    this.previews.clear();
    this.repairProfileIds.clear();
  }

  async rename(input: RenameInput): Promise<ProfileRedacted> {
    this.checkFail("rename");
    const existing = this.profiles.get(input.profileId);
    if (existing === undefined) {
      throw new Error("profile not found (redacted)");
    }
    const updated: ProfileRedacted = { ...existing, name: input.newName };
    this.profiles.set(input.profileId, updated);
    return updated;
  }

  async remove(input: { readonly profileId: string }): Promise<SelectResult> {
    this.checkFail("remove");
    this.profiles.delete(input.profileId);
    if (this.activeProfileId === input.profileId) {
      const remaining = [...this.profiles.values()];
      this.activeProfileId =
        remaining.length > 0 ? (remaining[0]?.id ?? null) : null;
    }
    this.generation += 1;
    return { profileId: this.activeProfileId, generation: this.generation };
  }

  async select(input: { readonly profileId: string }): Promise<SelectResult> {
    this.checkFail("select");
    if (!this.profiles.has(input.profileId)) {
      throw new Error("profile not found (redacted)");
    }
    this.activeProfileId = input.profileId;
    this.generation += 1;
    return { profileId: this.activeProfileId, generation: this.generation };
  }

  async health(): Promise<HealthSnapshot> {
    this.checkFail("health");
    if (this.healthGateArmed) {
      this.healthGateArmed = false;
      return new Promise<HealthSnapshot>((resolve) => {
        this.pendingHealthResolve = resolve;
      });
    }
    return {
      activeProfileId: this.activeProfileId,
      profiles: [...this.profiles.values()],
      generation: this.generation,
    };
  }

  // -- Test helpers ---------------------------------------------------------

  /** Recorded previewIds passed to cancelPreview, in call order. */
  readonly cancelPreviewCalls: string[] = [];
  /** Redacted opaque IDs returned by preview calls, in issue order. */
  readonly previewIdsIssued: string[] = [];
  /** Number of sensitive paste invocations that crossed the service boundary. */
  previewPasteCallCount = 0;

  snapshot(): {
    profiles: ProfileRedacted[];
    activeProfileId: string | null;
    generation: number;
  } {
    return {
      profiles: [...this.profiles.values()],
      activeProfileId: this.activeProfileId,
      generation: this.generation,
    };
  }
}

/**
 * Parse an authorization URL to a redacted origin `scheme://host[:port]`.
 * Mirrors the production parser's non-secret summary. Never returns the token
 * or query.
 */
export function parseOrigin(raw: string): string {
  try {
    const url = new URL(raw);
    const port = url.port ? `:${url.port}` : "";
    return `${url.protocol}//${url.hostname}${port}`;
  } catch {
    return "invalid";
  }
}

export const SAMPLE_AUTH_URL_HTTPS =
  "https://hub.example.com:8443/auth?token=SECRET_TOKEN_VALUE_123456";
export const SAMPLE_AUTH_URL_HTTP =
  "http://192.168.1.10:8080/auth?token=PRIVATE_TOKEN_VALUE_7890";
export const SECRET_TOKEN = "SECRET_TOKEN_VALUE_123456";
