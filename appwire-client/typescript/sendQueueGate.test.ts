// @vitest-environment node
//
// The shared send/queue gate's oracle, copied row by row from
// docs/superpowers/plans/2026-10-24-client-state-send-queue-gate.md §5 and §3.
// Every expected action and reason below is a literal from the plan; nothing
// here derives an expectation by running sendQueueGate on some other input or
// by reimplementing its branches.
import { describe, expect, test } from "vitest";
import {
  BROWSER_SEND_QUEUE_POLICY,
  NATIVE_SEND_QUEUE_POLICY,
  type SendQueueAction,
  type SendQueueDecision,
  type SendQueuePolicy,
  type SendQueueRecovery,
  sendQueueGate,
} from "./sendQueueGate";
import type { ThreadCapabilities } from "./types.gen";

type SendQueueCapabilities = Pick<ThreadCapabilities, "send" | "queue">;

function gate(
  statusType: string,
  send: boolean,
  queue: boolean,
  hasPendingSend: boolean,
  connected: boolean,
  recovery: SendQueueRecovery,
  policy: SendQueuePolicy,
): SendQueueDecision {
  const capabilities: SendQueueCapabilities = { send, queue };
  return sendQueueGate({ statusType, capabilities, hasPendingSend, connected, recovery, policy });
}

function action(
  statusType: string,
  send: boolean,
  queue: boolean,
  hasPendingSend: boolean,
  connected: boolean,
  recovery: SendQueueRecovery,
  policy: SendQueuePolicy,
): SendQueueAction {
  return gate(statusType, send, queue, hasPendingSend, connected, recovery, policy).action;
}

// Status classes collapse the statuses no branch distinguishes (§5 "Method and
// coverage"). "futureStatus" stands for any status outside the table's known
// sets.
const IDLE_SNAPSHOT_STATUSES = ["idle", "awaiting", "warning", "systemError"] as const;
const ACTIVE_STATUSES = ["active"] as const;
const NOT_LOADED_STATUSES = ["notLoaded"] as const;
const TERMINAL_STATUSES = ["ended", "closed"] as const;
const RESTART_STATUSES = ["restartRequired"] as const;
const UNKNOWN_STATUSES = ["futureStatus"] as const;

const ANY_NON_RESTART_STATUSES = [
  ...IDLE_SNAPSHOT_STATUSES,
  ...ACTIVE_STATUSES,
  ...NOT_LOADED_STATUSES,
  ...TERMINAL_STATUSES,
  ...UNKNOWN_STATUSES,
] as const;
const LIVE_OR_ACTIVE_STATUSES = [...IDLE_SNAPSHOT_STATUSES, ...ACTIVE_STATUSES] as const;
const NON_LIVE_SNAPSHOT_STATUSES = [...NOT_LOADED_STATUSES, ...TERMINAL_STATUSES, ...UNKNOWN_STATUSES] as const;

interface BaseMatrixRow {
  readonly statuses: readonly string[];
  readonly send: boolean;
  readonly queue: boolean;
  readonly pending: boolean;
  /** The plan's "browser new" column. The plan's "browser old" column is the
   *  same for every reachable row (diff = none). */
  readonly browser: SendQueueAction;
  /** The plan's "native new online" column. Its "native old online" column is
   *  the same for every reachable row (diff = none). */
  readonly nativeOnline: SendQueueAction;
  /** The plan's "native new offline" column. Its "native old offline" column
   *  is the same for every reachable row (diff = none). */
  readonly nativeOffline: SendQueueAction;
}

// §5 "Base matrix (no recovery; browser online; native online and offline)".
// 48 literal rows: six status classes times the eight send/queue/pending
// combinations. The browser is online only; native tests both connectivities.
const BASE_MATRIX: readonly BaseMatrixRow[] = [
  // idle, awaiting, warning, systemError
  {
    statuses: IDLE_SNAPSHOT_STATUSES,
    send: true,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: IDLE_SNAPSHOT_STATUSES,
    send: true,
    queue: true,
    pending: false,
    browser: "send",
    nativeOnline: "send",
    nativeOffline: "queue",
  },
  {
    statuses: IDLE_SNAPSHOT_STATUSES,
    send: true,
    queue: false,
    pending: true,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: IDLE_SNAPSHOT_STATUSES,
    send: true,
    queue: false,
    pending: false,
    browser: "send",
    nativeOnline: "send",
    nativeOffline: "none",
  },
  {
    statuses: IDLE_SNAPSHOT_STATUSES,
    send: false,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: IDLE_SNAPSHOT_STATUSES,
    send: false,
    queue: true,
    pending: false,
    browser: "send",
    nativeOnline: "none",
    nativeOffline: "queue",
  },
  {
    statuses: IDLE_SNAPSHOT_STATUSES,
    send: false,
    queue: false,
    pending: true,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: IDLE_SNAPSHOT_STATUSES,
    send: false,
    queue: false,
    pending: false,
    browser: "send",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  // active
  {
    statuses: ACTIVE_STATUSES,
    send: true,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: ACTIVE_STATUSES,
    send: true,
    queue: true,
    pending: false,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: ACTIVE_STATUSES,
    send: true,
    queue: false,
    pending: true,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: ACTIVE_STATUSES,
    send: true,
    queue: false,
    pending: false,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: ACTIVE_STATUSES,
    send: false,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: ACTIVE_STATUSES,
    send: false,
    queue: true,
    pending: false,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: ACTIVE_STATUSES,
    send: false,
    queue: false,
    pending: true,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: ACTIVE_STATUSES,
    send: false,
    queue: false,
    pending: false,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  // notLoaded
  {
    statuses: NOT_LOADED_STATUSES,
    send: true,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: NOT_LOADED_STATUSES,
    send: true,
    queue: true,
    pending: false,
    browser: "send",
    nativeOnline: "resume",
    nativeOffline: "resume",
  },
  {
    statuses: NOT_LOADED_STATUSES,
    send: true,
    queue: false,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: NOT_LOADED_STATUSES,
    send: true,
    queue: false,
    pending: false,
    browser: "send",
    nativeOnline: "resume",
    nativeOffline: "resume",
  },
  {
    statuses: NOT_LOADED_STATUSES,
    send: false,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: NOT_LOADED_STATUSES,
    send: false,
    queue: true,
    pending: false,
    browser: "send",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: NOT_LOADED_STATUSES,
    send: false,
    queue: false,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: NOT_LOADED_STATUSES,
    send: false,
    queue: false,
    pending: false,
    browser: "send",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  // ended, closed
  {
    statuses: TERMINAL_STATUSES,
    send: true,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: TERMINAL_STATUSES,
    send: true,
    queue: true,
    pending: false,
    browser: "send",
    nativeOnline: "resume",
    nativeOffline: "resume",
  },
  {
    statuses: TERMINAL_STATUSES,
    send: true,
    queue: false,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: TERMINAL_STATUSES,
    send: true,
    queue: false,
    pending: false,
    browser: "send",
    nativeOnline: "resume",
    nativeOffline: "resume",
  },
  {
    statuses: TERMINAL_STATUSES,
    send: false,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: TERMINAL_STATUSES,
    send: false,
    queue: true,
    pending: false,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: TERMINAL_STATUSES,
    send: false,
    queue: false,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: TERMINAL_STATUSES,
    send: false,
    queue: false,
    pending: false,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  // restartRequired: every combination answers none, before any recovery face.
  {
    statuses: RESTART_STATUSES,
    send: true,
    queue: true,
    pending: true,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: RESTART_STATUSES,
    send: true,
    queue: true,
    pending: false,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: RESTART_STATUSES,
    send: true,
    queue: false,
    pending: true,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: RESTART_STATUSES,
    send: true,
    queue: false,
    pending: false,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: RESTART_STATUSES,
    send: false,
    queue: true,
    pending: true,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: RESTART_STATUSES,
    send: false,
    queue: true,
    pending: false,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: RESTART_STATUSES,
    send: false,
    queue: false,
    pending: true,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  {
    statuses: RESTART_STATUSES,
    send: false,
    queue: false,
    pending: false,
    browser: "none",
    nativeOnline: "none",
    nativeOffline: "none",
  },
  // unknown/future
  {
    statuses: UNKNOWN_STATUSES,
    send: true,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: UNKNOWN_STATUSES,
    send: true,
    queue: true,
    pending: false,
    browser: "send",
    nativeOnline: "send",
    nativeOffline: "queue",
  },
  {
    statuses: UNKNOWN_STATUSES,
    send: true,
    queue: false,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: UNKNOWN_STATUSES,
    send: true,
    queue: false,
    pending: false,
    browser: "send",
    nativeOnline: "send",
    nativeOffline: "queue",
  },
  {
    statuses: UNKNOWN_STATUSES,
    send: false,
    queue: true,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: UNKNOWN_STATUSES,
    send: false,
    queue: true,
    pending: false,
    browser: "send",
    nativeOnline: "none",
    nativeOffline: "queue",
  },
  {
    statuses: UNKNOWN_STATUSES,
    send: false,
    queue: false,
    pending: true,
    browser: "queue",
    nativeOnline: "queue",
    nativeOffline: "queue",
  },
  {
    statuses: UNKNOWN_STATUSES,
    send: false,
    queue: false,
    pending: false,
    browser: "send",
    nativeOnline: "none",
    nativeOffline: "queue",
  },
];

const BASE_CASES = BASE_MATRIX.flatMap((row) => row.statuses.map((statusType) => ({ ...row, statusType })));

describe("base matrix: no recovery, browser online, native online and offline (§5)", () => {
  test.each(BASE_CASES)(
    "base row $statusType send=$send queue=$queue pending=$pending -> browser $browser, native online $nativeOnline, native offline $nativeOffline",
    ({ statusType, send, queue, pending, browser, nativeOnline, nativeOffline }) => {
      expect(action(statusType, send, queue, pending, true, { kind: "none" }, BROWSER_SEND_QUEUE_POLICY)).toBe(browser);
      expect(action(statusType, send, queue, pending, true, { kind: "none" }, NATIVE_SEND_QUEUE_POLICY)).toBe(
        nativeOnline,
      );
      expect(action(statusType, send, queue, pending, false, { kind: "none" }, NATIVE_SEND_QUEUE_POLICY)).toBe(
        nativeOffline,
      );
    },
  );
});

interface BrowserRecoveryRow {
  readonly recovery: "blocked" | "send-resumes" | "folded-resume";
  readonly statuses: readonly string[];
  readonly send: boolean;
  readonly queue: boolean;
  readonly pending: boolean;
  /** §5 "Recovery matrix": browser new. The browser old column is the same for
   *  every reachable row (diff = none; the one intentional change is the
   *  unreachable restartRequired + folded-resume pair tested below). */
  readonly expected: SendQueueAction;
}

// §5 "Recovery matrix (old versus new)": the three browser faces, invariant
// across every reachable non-restart status and across connectivity ("*").
const BROWSER_RECOVERY_MATRIX: readonly BrowserRecoveryRow[] = [
  // blocked: none, with or without a pending send and whatever the caps.
  { recovery: "blocked", statuses: ANY_NON_RESTART_STATUSES, send: true, queue: true, pending: true, expected: "none" },
  {
    recovery: "blocked",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: false,
    pending: true,
    expected: "none",
  },
  {
    recovery: "blocked",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: true,
    pending: true,
    expected: "none",
  },
  {
    recovery: "blocked",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: false,
    pending: true,
    expected: "none",
  },
  {
    recovery: "blocked",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: true,
    pending: false,
    expected: "none",
  },
  {
    recovery: "blocked",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: false,
    pending: false,
    expected: "none",
  },
  {
    recovery: "blocked",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: true,
    pending: false,
    expected: "none",
  },
  {
    recovery: "blocked",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: false,
    pending: false,
    expected: "none",
  },
  // send-resumes: send, whatever the table, caps or pending send say.
  {
    recovery: "send-resumes",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: true,
    pending: true,
    expected: "send",
  },
  {
    recovery: "send-resumes",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: false,
    pending: true,
    expected: "send",
  },
  {
    recovery: "send-resumes",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: true,
    pending: true,
    expected: "send",
  },
  {
    recovery: "send-resumes",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: false,
    pending: true,
    expected: "send",
  },
  {
    recovery: "send-resumes",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: true,
    pending: false,
    expected: "send",
  },
  {
    recovery: "send-resumes",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: false,
    pending: false,
    expected: "send",
  },
  {
    recovery: "send-resumes",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: true,
    pending: false,
    expected: "send",
  },
  {
    recovery: "send-resumes",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: false,
    pending: false,
    expected: "send",
  },
  // folded-resume: Send with no pending send; none with one.
  {
    recovery: "folded-resume",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: true,
    pending: true,
    expected: "none",
  },
  {
    recovery: "folded-resume",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: false,
    pending: true,
    expected: "none",
  },
  {
    recovery: "folded-resume",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: true,
    pending: true,
    expected: "none",
  },
  {
    recovery: "folded-resume",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: false,
    pending: true,
    expected: "none",
  },
  {
    recovery: "folded-resume",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: true,
    pending: false,
    expected: "send",
  },
  {
    recovery: "folded-resume",
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: false,
    pending: false,
    expected: "send",
  },
  {
    recovery: "folded-resume",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: true,
    pending: false,
    expected: "send",
  },
  {
    recovery: "folded-resume",
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: false,
    pending: false,
    expected: "send",
  },
];

const BROWSER_RECOVERY_CASES = BROWSER_RECOVERY_MATRIX.flatMap((row) =>
  row.statuses.flatMap((statusType) => [true, false].map((connected) => ({ ...row, statusType, connected }))),
);

describe("recovery matrix: browser faces, online and offline (§5)", () => {
  test.each(BROWSER_RECOVERY_CASES)(
    "recovery $recovery status=$statusType send=$send queue=$queue pending=$pending connected=$connected -> $expected",
    ({ recovery, statusType, send, queue, pending, connected, expected }) => {
      expect(action(statusType, send, queue, pending, connected, { kind: recovery }, BROWSER_SEND_QUEUE_POLICY)).toBe(
        expected,
      );
    },
  );
});

interface NativeExplicitResumeRow {
  readonly statuses: readonly string[];
  readonly send: boolean;
  readonly queue: boolean;
  readonly pending: boolean;
  readonly connected: boolean;
  /** §5 "Recovery matrix": native new for explicit-resume. The native old
   *  column is the same for every row (diff = none). */
  readonly expected: SendQueueAction;
}

// §5 "Recovery matrix": native explicit-resume. Invariant across status except
// when a pending send consults the table and the queue bit is false: a live
// snapshot then answers none, while a non-live status (notLoaded, terminal,
// unknown) keeps the tier-6 queue because its false queue bit is a hub stub,
// not the daemon's answer.
const NATIVE_EXPLICIT_RESUME_MATRIX: readonly NativeExplicitResumeRow[] = [
  { statuses: ANY_NON_RESTART_STATUSES, send: true, queue: true, pending: true, connected: true, expected: "queue" },
  { statuses: ANY_NON_RESTART_STATUSES, send: true, queue: true, pending: true, connected: false, expected: "queue" },
  {
    statuses: LIVE_OR_ACTIVE_STATUSES,
    send: true,
    queue: false,
    pending: true,
    connected: true,
    expected: "none",
  },
  {
    statuses: LIVE_OR_ACTIVE_STATUSES,
    send: true,
    queue: false,
    pending: true,
    connected: false,
    expected: "none",
  },
  { statuses: NON_LIVE_SNAPSHOT_STATUSES, send: true, queue: false, pending: true, connected: true, expected: "queue" },
  {
    statuses: NON_LIVE_SNAPSHOT_STATUSES,
    send: true,
    queue: false,
    pending: true,
    connected: false,
    expected: "queue",
  },
  { statuses: ANY_NON_RESTART_STATUSES, send: false, queue: true, pending: true, connected: true, expected: "queue" },
  { statuses: ANY_NON_RESTART_STATUSES, send: false, queue: true, pending: true, connected: false, expected: "queue" },
  {
    statuses: LIVE_OR_ACTIVE_STATUSES,
    send: false,
    queue: false,
    pending: true,
    connected: true,
    expected: "none",
  },
  {
    statuses: LIVE_OR_ACTIVE_STATUSES,
    send: false,
    queue: false,
    pending: true,
    connected: false,
    expected: "none",
  },
  {
    statuses: NON_LIVE_SNAPSHOT_STATUSES,
    send: false,
    queue: false,
    pending: true,
    connected: true,
    expected: "queue",
  },
  {
    statuses: NON_LIVE_SNAPSHOT_STATUSES,
    send: false,
    queue: false,
    pending: true,
    connected: false,
    expected: "queue",
  },
  { statuses: ANY_NON_RESTART_STATUSES, send: true, queue: true, pending: false, connected: true, expected: "resume" },
  { statuses: ANY_NON_RESTART_STATUSES, send: true, queue: true, pending: false, connected: false, expected: "resume" },
  { statuses: ANY_NON_RESTART_STATUSES, send: true, queue: false, pending: false, connected: true, expected: "resume" },
  {
    statuses: ANY_NON_RESTART_STATUSES,
    send: true,
    queue: false,
    pending: false,
    connected: false,
    expected: "resume",
  },
  { statuses: ANY_NON_RESTART_STATUSES, send: false, queue: true, pending: false, connected: true, expected: "resume" },
  {
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: true,
    pending: false,
    connected: false,
    expected: "resume",
  },
  {
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: false,
    pending: false,
    connected: true,
    expected: "resume",
  },
  {
    statuses: ANY_NON_RESTART_STATUSES,
    send: false,
    queue: false,
    pending: false,
    connected: false,
    expected: "resume",
  },
];

const NATIVE_EXPLICIT_RESUME_CASES = NATIVE_EXPLICIT_RESUME_MATRIX.flatMap((row) =>
  row.statuses.map((statusType) => ({ ...row, statusType })),
);

describe("recovery matrix: native explicit-resume (§5)", () => {
  test.each(NATIVE_EXPLICIT_RESUME_CASES)(
    "explicit-resume status=$statusType send=$send queue=$queue pending=$pending connected=$connected -> $expected",
    ({ statusType, send, queue, pending, connected, expected }) => {
      expect(
        action(statusType, send, queue, pending, connected, { kind: "explicit-resume" }, NATIVE_SEND_QUEUE_POLICY),
      ).toBe(expected);
    },
  );
});

interface TransitionRow {
  readonly name: string;
  readonly statusType: string;
  readonly send: boolean;
  readonly queue: boolean;
  readonly pending: boolean;
  readonly connected: boolean;
  readonly recovery: SendQueueRecovery;
  readonly policy: SendQueuePolicy;
  readonly expected: SendQueueDecision;
}

// §3 "Precedence contract" and §6 "Precedence transitions". The first matching
// row wins. blocked/send-resumes/folded/explicit are one union value, so their
// relative order is structural; each row here pins that face's answer against
// the table, offline, or online answer it would otherwise receive.
const TRANSITIONS: readonly TransitionRow[] = [
  // restart before every recovery face and before connectivity.
  {
    name: "restartRequired answers none before explicit-resume, even offline",
    statusType: "restartRequired",
    send: true,
    queue: true,
    pending: false,
    connected: false,
    recovery: { kind: "explicit-resume" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "restart-required" },
  },
  {
    name: "restartRequired answers none before blocked",
    statusType: "restartRequired",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "blocked" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "restart-required" },
  },
  {
    name: "restartRequired answers none before send-resumes",
    statusType: "restartRequired",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "send-resumes" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "restart-required" },
  },
  {
    name: "restartRequired answers none before folded-resume",
    statusType: "restartRequired",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "folded-resume" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "restart-required" },
  },
  {
    name: "restartRequired answers none before the table's plain send",
    statusType: "restartRequired",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "restart-required" },
  },
  // blocked refuses the queue and send the table would allow.
  {
    name: "blocked refuses a queueable active session",
    statusType: "active",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "blocked" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "recovery-fenced" },
  },
  {
    name: "blocked refuses offline before the offline branch",
    statusType: "idle",
    send: true,
    queue: true,
    pending: false,
    connected: false,
    recovery: { kind: "blocked" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "recovery-fenced" },
  },
  // send-resumes sends where the table would refuse or queue.
  {
    name: "send-resumes sends with a pending send and no capabilities",
    statusType: "idle",
    send: false,
    queue: false,
    pending: true,
    connected: true,
    recovery: { kind: "send-resumes" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "send", reason: "recovery-send" },
  },
  {
    name: "send-resumes sends offline before the offline branch",
    statusType: "idle",
    send: false,
    queue: false,
    pending: false,
    connected: false,
    recovery: { kind: "send-resumes" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "send", reason: "recovery-send" },
  },
  // folded-resume with/without a pending send.
  {
    name: "folded-resume refuses a second send",
    statusType: "active",
    send: true,
    queue: true,
    pending: true,
    connected: true,
    recovery: { kind: "folded-resume" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "pending-send-in-flight" },
  },
  {
    name: "folded-resume sends the first message",
    statusType: "idle",
    send: false,
    queue: false,
    pending: false,
    connected: true,
    recovery: { kind: "folded-resume" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "send", reason: "resume-folded" },
  },
  {
    name: "folded-resume sends offline before the offline branch",
    statusType: "idle",
    send: false,
    queue: false,
    pending: false,
    connected: false,
    recovery: { kind: "folded-resume" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "send", reason: "resume-folded" },
  },
  // explicit-resume without a pending send, online and offline.
  {
    name: "explicit-resume resumes without the send capability",
    statusType: "idle",
    send: false,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "explicit-resume" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "resume", reason: "resume" },
  },
  {
    name: "explicit-resume resumes offline before the offline branch",
    statusType: "idle",
    send: false,
    queue: false,
    pending: false,
    connected: false,
    recovery: { kind: "explicit-resume" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "resume", reason: "resume" },
  },
  // explicit-resume with a pending send: the table decides, queue seam or not.
  {
    name: "explicit-resume queues a message behind the first where the harness can",
    statusType: "active",
    send: true,
    queue: true,
    pending: true,
    connected: true,
    recovery: { kind: "explicit-resume" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "queue", reason: "queue" },
  },
  {
    name: "explicit-resume refuses a parked message with no live queue seam",
    statusType: "active",
    send: true,
    queue: false,
    pending: true,
    connected: true,
    recovery: { kind: "explicit-resume" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "queue-unavailable" },
  },
  {
    name: "explicit-resume queues on a non-live status with no queue seam",
    statusType: "futureStatus",
    send: true,
    queue: false,
    pending: true,
    connected: true,
    recovery: { kind: "explicit-resume" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "queue", reason: "queue" },
  },
  {
    name: "explicit-resume keeps its pending-send queue answer offline",
    statusType: "active",
    send: true,
    queue: true,
    pending: true,
    connected: false,
    recovery: { kind: "explicit-resume" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "queue", reason: "queue" },
  },
  // offline shut-down first message: resume only with the send capability.
  {
    name: "offline shut-down first message resumes when send is advertised",
    statusType: "notLoaded",
    send: true,
    queue: false,
    pending: false,
    connected: false,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "resume", reason: "resume" },
  },
  {
    name: "offline shut-down first message refuses without send",
    statusType: "closed",
    send: false,
    queue: true,
    pending: false,
    connected: false,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "send-unavailable" },
  },
  // offline queue and its refusal.
  {
    name: "offline queues a message where the harness can",
    statusType: "idle",
    send: true,
    queue: true,
    pending: false,
    connected: false,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "queue", reason: "queue" },
  },
  {
    name: "offline refuses where the harness cannot queue",
    statusType: "idle",
    send: true,
    queue: false,
    pending: false,
    connected: false,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "offline-no-queue" },
  },
  {
    name: "offline queues on a non-live status even without a queue seam",
    statusType: "futureStatus",
    send: true,
    queue: false,
    pending: false,
    connected: false,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "queue", reason: "queue" },
  },
  // online queue.
  {
    name: "online queues behind a running turn",
    statusType: "active",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "queue", reason: "queue" },
  },
  // shut-down send versus resume: the two policy constants' difference.
  {
    name: "shut-down first message is a send in the browser",
    statusType: "ended",
    send: true,
    queue: false,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "send", reason: "shut-down-send" },
  },
  {
    name: "shut-down first message is a resume in native",
    statusType: "ended",
    send: true,
    queue: false,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "resume", reason: "resume" },
  },
  {
    name: "cold notLoaded first message is a send in the browser",
    statusType: "notLoaded",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "send", reason: "shut-down-send" },
  },
  {
    name: "cold notLoaded first message is a resume in native",
    statusType: "notLoaded",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "resume", reason: "resume" },
  },
  // plain send with and without the required capability.
  {
    name: "plain send with the send capability (native)",
    statusType: "idle",
    send: true,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "send", reason: "plain-send" },
  },
  {
    name: "plain send without the send capability (native)",
    statusType: "idle",
    send: false,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "send-unavailable" },
  },
  {
    name: "plain send ignores the send capability in the browser",
    statusType: "idle",
    send: false,
    queue: true,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: BROWSER_SEND_QUEUE_POLICY,
    expected: { action: "send", reason: "plain-send" },
  },
  {
    name: "no action when the active table refuses both",
    statusType: "active",
    send: false,
    queue: false,
    pending: false,
    connected: true,
    recovery: { kind: "none" },
    policy: NATIVE_SEND_QUEUE_POLICY,
    expected: { action: "none", reason: "no-action" },
  },
];

describe("precedence transitions (§3)", () => {
  test.each(TRANSITIONS)("$name", (row) => {
    expect(gate(row.statusType, row.send, row.queue, row.pending, row.connected, row.recovery, row.policy)).toEqual(
      row.expected,
    );
  });

  // restartRequired is first, so no recovery face can move it.
  test.each([
    ["none", true],
    ["none", false],
    ["blocked", true],
    ["blocked", false],
    ["send-resumes", true],
    ["send-resumes", false],
    ["folded-resume", true],
    ["folded-resume", false],
    ["explicit-resume", true],
    ["explicit-resume", false],
  ] as const)("restartRequired answers none for recovery %s, connected %s", (kind, connected) => {
    expect(gate("restartRequired", true, true, false, connected, { kind }, NATIVE_SEND_QUEUE_POLICY)).toEqual({
      action: "none",
      reason: "restart-required",
    });
  });
});

describe("client policy constants (§3)", () => {
  test("the browser policy leaves plain send ungated and folds a shut-down resume into Send", () => {
    expect(BROWSER_SEND_QUEUE_POLICY).toEqual({
      sendRequiresCapability: false,
      shutDownAction: "send",
    });
  });

  test("the native policy requires send for a plain send and resumes a shut-down session explicitly", () => {
    expect(NATIVE_SEND_QUEUE_POLICY).toEqual({
      sendRequiresCapability: true,
      shutDownAction: "resume",
    });
  });

  test("the same shut-down session answers send under one policy and resume under the other", () => {
    const browser = gate("closed", true, false, false, true, { kind: "none" }, BROWSER_SEND_QUEUE_POLICY);
    const native = gate("closed", true, false, false, true, { kind: "none" }, NATIVE_SEND_QUEUE_POLICY);
    expect(browser).toEqual({ action: "send", reason: "shut-down-send" });
    expect(native).toEqual({ action: "resume", reason: "resume" });
  });
});

interface ParityRow {
  readonly statusType: string;
  readonly send: boolean;
  readonly queue: boolean;
  readonly pending: false;
  readonly browser: SendQueueAction;
  readonly nativeOnline: SendQueueAction;
}

// The 18 existing browser/native differences from §1, as literal parity rows.
// They are policy inputs, not gate defects, and a future "cleanup" must not
// silently unify them (plan §5 "Differences", §7 non-goals).
const PARITY_ROWS: readonly ParityRow[] = [
  // A plain send ignores capabilities.send in the browser, requires it in
  // native: six statuses times two queue bits, no pending send.
  { statusType: "idle", send: false, queue: true, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "idle", send: false, queue: false, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "awaiting", send: false, queue: true, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "awaiting", send: false, queue: false, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "warning", send: false, queue: true, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "warning", send: false, queue: false, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "systemError", send: false, queue: true, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "systemError", send: false, queue: false, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "notLoaded", send: false, queue: true, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "notLoaded", send: false, queue: false, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "futureStatus", send: false, queue: true, pending: false, browser: "send", nativeOnline: "none" },
  { statusType: "futureStatus", send: false, queue: false, pending: false, browser: "send", nativeOnline: "none" },
  // A shut-down first message sends in the browser and explicitly resumes in
  // native: three shut-down statuses times two queue bits.
  { statusType: "notLoaded", send: true, queue: true, pending: false, browser: "send", nativeOnline: "resume" },
  { statusType: "notLoaded", send: true, queue: false, pending: false, browser: "send", nativeOnline: "resume" },
  { statusType: "ended", send: true, queue: true, pending: false, browser: "send", nativeOnline: "resume" },
  { statusType: "ended", send: true, queue: false, pending: false, browser: "send", nativeOnline: "resume" },
  { statusType: "closed", send: true, queue: true, pending: false, browser: "send", nativeOnline: "resume" },
  { statusType: "closed", send: true, queue: false, pending: false, browser: "send", nativeOnline: "resume" },
];

describe("the 18 existing browser/native differences stay explicit (§1)", () => {
  test("the parity table has all 18 rows", () => {
    expect(PARITY_ROWS).toHaveLength(18);
  });

  test.each(PARITY_ROWS)(
    "parity row status=$statusType send=$send queue=$queue -> browser $browser, native online $nativeOnline",
    ({ statusType, send, queue, pending, browser, nativeOnline }) => {
      expect(action(statusType, send, queue, pending, true, { kind: "none" }, BROWSER_SEND_QUEUE_POLICY)).toBe(browser);
      expect(action(statusType, send, queue, pending, true, { kind: "none" }, NATIVE_SEND_QUEUE_POLICY)).toBe(
        nativeOnline,
      );
    },
  );
});

describe("the two unreachable restartRequired + browser recovery pairs (§3, §5)", () => {
  // The one intentional answer change: old browser would have answered send,
  // the gate answers none. The hub never stamps ResumeOnlyFoldable for a
  // restart-required daemon (cmd/evener-hub/app_turn_start_resume_only_test.go:223),
  // so no real snapshot reaches this pair.
  test("restartRequired + folded-resume with no pending send answers none", () => {
    expect(
      gate("restartRequired", true, true, false, true, { kind: "folded-resume" }, BROWSER_SEND_QUEUE_POLICY),
    ).toEqual({ action: "none", reason: "restart-required" });
  });

  // The other synthetic pair: old browser would also have answered send.
  // isSendResumes explicitly excludes restartRequired (threads.ts:4419).
  test("restartRequired + send-resumes answers none", () => {
    expect(
      gate("restartRequired", true, true, false, true, { kind: "send-resumes" }, BROWSER_SEND_QUEUE_POLICY),
    ).toEqual({ action: "none", reason: "restart-required" });
  });
});
