/**
 * Deterministic visual fixtures for browser geometry tests.
 *
 * Each fixture provides the exact data shape the screen components expect, so
 * the geometry runner can render components in jsdom and measure their layout
 * without a Hub, Tauri plugin, or live network. No credential, raw URL, or
 * token is ever held in fixture state.
 *
 * Fixture routes:
 * - roster: SessionsScreen with sessions in various states
 * - conversation: long/streaming ConversationScreen timeline
 * - activity: ConversationScreen with the activity sheet open
 * - ask: ConversationScreen with askPending (structured questions)
 * - attachments: ConversationScreen with an attachment strip
 * - new: NewSessionScreen form
 * - settings: SettingsScreen
 *
 * Each fixture is deterministic — the same fixture always produces the same
 * data, so geometry measurements are reproducible.
 */

import type {
  AskBatch,
  MobileCapabilities,
  MobileConversation,
  MobileTimelineItem,
  MobileUsage,
} from "../conversation/model";
import type { ActivityView } from "../services/activity";
import type { ProfileRedacted } from "../services/nativeProfiles";
import type { Reachability } from "../state/connection";

// ---------------------------------------------------------------------------
// Shared constants
// ---------------------------------------------------------------------------

const ALL_TRUE_CAPS: MobileCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  rename: true,
};

const ALL_FALSE_CAPS: MobileCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  rename: false,
};

const SAMPLE_USAGE: MobileUsage = {
  inputTokens: 12500,
  outputTokens: 8200,
  cacheReadTokens: 3100,
  totalTokens: 23800,
  cost: "$0.042",
  contextUsed: 41000,
  contextWindow: 200000,
  contextRemaining: 159000,
  contextPressure: 0.21,
};

// ---------------------------------------------------------------------------
// Fixture route type
// ---------------------------------------------------------------------------

export type ConversationFixtureRoute =
  | "roster"
  | "conversation"
  | "activity"
  | "ask"
  | "attachments"
  | "new"
  | "settings";

export function isConversationFixtureRoute(
  value: string | null,
): value is ConversationFixtureRoute {
  return (
    value === "roster" ||
    value === "conversation" ||
    value === "activity" ||
    value === "ask" ||
    value === "attachments" ||
    value === "new" ||
    value === "settings"
  );
}

// ---------------------------------------------------------------------------
// Roster fixture: sessions with various states
// ---------------------------------------------------------------------------

export interface RosterSession {
  readonly id: string;
  readonly title: string;
  readonly preview: string;
  readonly status: "needs-you" | "running" | "recent";
  readonly updatedAt: string;
}

export interface RosterFixture {
  readonly route: "roster";
  readonly profiles: readonly ProfileRedacted[];
  readonly activeProfileId: string;
  readonly reachability: Record<string, Reachability>;
  readonly sessions: readonly RosterSession[];
}

function rosterFixture(): RosterFixture {
  return {
    route: "roster",
    profiles: [
      { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
      { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
    ],
    activeProfileId: "p1",
    reachability: { p1: "reachable", p2: "unreachable" },
    sessions: [
      {
        id: "s1",
        title: "Fix auth bug in middleware",
        preview: "The auth middleware fails on token refresh when...",
        status: "needs-you",
        updatedAt: "2026-08-23T12:45:00Z",
      },
      {
        id: "s2",
        title: "Refactor database layer",
        preview: "Running: migrating query builders to use prepared...",
        status: "running",
        updatedAt: "2026-08-23T12:30:00Z",
      },
      {
        id: "s3",
        title: "Add integration tests",
        preview: "Added 12 new test cases covering edge conditions",
        status: "recent",
        updatedAt: "2026-08-23T11:15:00Z",
      },
      {
        id: "s4",
        title: "Update API documentation",
        preview: "Documented all new endpoints in the v2 API surface",
        status: "recent",
        updatedAt: "2026-08-23T10:00:00Z",
      },
    ],
  };
}

// ---------------------------------------------------------------------------
// Conversation fixture: long/streaming timeline
// ---------------------------------------------------------------------------

function buildConversationItems(): MobileTimelineItem[] {
  const items: MobileTimelineItem[] = [];

  // User message
  items.push({
    kind: "user",
    id: "u-1",
    text: "Can you help me debug the authentication middleware?",
  });

  // Assistant message (streaming)
  items.push({
    kind: "assistant",
    id: "a-1",
    markdown:
      "I'll help you debug the authentication middleware. Let me start by examining the relevant files.",
    streaming: false,
  });

  // Activity: shell tool call
  items.push({
    kind: "activity",
    id: "act-1",
    label: "find auth files",
    family: "tool",
    state: "completed",
    detail: {
      arguments: '{"path": "src/middleware", "pattern": "auth"}',
      output: "src/middleware/auth.go\nsrc/middleware/auth_test.go",
      exitCode: 0,
      durationMs: 120,
      callId: "call-1",
    },
  });

  // Activity: reasoning
  items.push({
    kind: "activity",
    id: "act-2",
    label: "analyzing auth flow",
    family: "reasoning",
    state: "completed",
    detail: {
      arguments: "",
      output: "Traced the token refresh path through 4 files",
      durationMs: 3400,
      callId: "call-2",
    },
  });

  // Notice
  items.push({
    kind: "notice",
    id: "n-1",
    origin: "steering",
    family: "informational",
    tone: "info",
    text: "Steered to focus on the token refresh path",
  });

  // Assistant message with more detail
  items.push({
    kind: "assistant",
    id: "a-2",
    markdown:
      "I found the issue. The token refresh logic in `auth.go` doesn't handle the case where the refresh token has expired. Here's the problematic code:\n\n```go\nfunc refreshToken(token string) (string, error) {\n    // Missing expiry check\n    return parseToken(token)\n}\n```\n\nWe need to add an expiry check before parsing.",
    streaming: false,
  });

  // Failure
  items.push({
    kind: "failure",
    id: "f-1",
    title: "Tool error",
    detail: "grep: invalid regex pattern in search",
  });

  // Activity: running tool call
  items.push({
    kind: "activity",
    id: "act-3",
    label: "applying fix",
    family: "tool",
    state: "running",
    detail: {
      arguments:
        '{"file": "src/middleware/auth.go", "edit": "add expiry check"}',
      callId: "call-3",
    },
  });

  // User follow-up
  items.push({
    kind: "user",
    id: "u-2",
    text: "Good catch! Can you also add a test for the expired token case?",
  });

  // Assistant streaming message
  items.push({
    kind: "assistant",
    id: "a-3",
    markdown:
      "I'll add a test case for the expired token scenario. Let me write",
    streaming: true,
  });

  // More items to exercise virtualization
  for (let i = 0; i < 20; i++) {
    items.push({
      kind: "activity",
      id: `act-batch-${i}`,
      label: `test iteration ${i + 1}`,
      family: "tool",
      state: i % 5 === 0 ? "failed" : "completed",
      detail: {
        arguments: `{"test": "case_${i + 1}"}`,
        output: i % 5 === 0 ? "test failed: assertion error" : "passed",
        exitCode: i % 5 === 0 ? 1 : 0,
        durationMs: 50 + i * 10,
        callId: `call-batch-${i}`,
      },
    });
  }

  // Final assistant message
  items.push({
    kind: "assistant",
    id: "a-final",
    markdown:
      "All tests pass now. The authentication middleware correctly handles expired refresh tokens, and we have 5 new test cases covering the edge conditions.",
    streaming: false,
  });

  return items;
}

export interface ConversationFixture {
  readonly route: "conversation";
  readonly conversation: MobileConversation;
  readonly draft: string;
}

function conversationFixture(): ConversationFixture {
  return {
    route: "conversation",
    conversation: {
      id: "thread-conv-1",
      sessionId: "session-conv-1",
      name: "Fix auth bug in middleware",
      preview: "The auth middleware fails on token refresh when...",
      modelProvider: "anthropic",
      status: "running",
      items: buildConversationItems(),
      capabilities: ALL_TRUE_CAPS,
      queue: { depth: 0, preview: [] },
      usage: SAMPLE_USAGE,
      askPending: false,
    },
    draft: "",
  };
}

// ---------------------------------------------------------------------------
// Activity sheet fixture: open activity sheet with tasks/work/usage
// ---------------------------------------------------------------------------

export interface ActivityFixture {
  readonly route: "activity";
  readonly conversation: MobileConversation;
  readonly activityView: ActivityView;
  readonly activityOpen: boolean;
}

function activityFixture(): ActivityFixture {
  return {
    route: "activity",
    conversation: {
      id: "thread-act-1",
      sessionId: "session-act-1",
      name: "Refactor database layer",
      preview: "Running: migrating query builders to use prepared...",
      modelProvider: "anthropic",
      status: "running",
      items: [
        {
          kind: "user",
          id: "u-act-1",
          text: "Refactor the database layer to use prepared statements",
        },
        {
          kind: "assistant",
          id: "a-act-1",
          markdown: "Starting the database refactoring now.",
          streaming: false,
        },
      ],
      capabilities: ALL_TRUE_CAPS,
      queue: { depth: 2, preview: ["Add migration tests", "Update docs"] },
      usage: SAMPLE_USAGE,
      askPending: false,
    },
    activityView: {
      tasks: [
        { status: "active", count: 2 },
        { status: "open", count: 5 },
        { status: "done", count: 12 },
      ],
      work: [
        {
          kind: "delegate",
          label: "subagent: fix auth bug",
          tone: "running",
          durationMs: 45000,
          outputSummary: "2.1 KB",
          children: [
            {
              kind: "job",
              label: "shell: run tests",
              tone: "terminal",
              durationMs: 3200,
              outputSummary: "8.5 KB",
            },
            {
              kind: "job",
              label: "shell: lint",
              tone: "failed",
              durationMs: 1200,
              outputSummary: "1.2 KB",
              diagnostics: {
                rawId: "job-1",
                operationName: "shell",
                statusClass: "failed",
                exitCode: 1,
                startedAt: "2026-08-23T12:44:00Z",
                endedAt: "2026-08-23T12:44:01Z",
                durationMs: 1200,
              },
            },
          ],
        },
        {
          kind: "watch",
          label: "watch: src/**/*.go",
          tone: "idle",
        },
      ],
      usage: {
        inputTokens: 12500,
        outputTokens: 8200,
        cacheReadTokens: 3100,
        totalTokens: 23800,
        cost: "$0.042",
        contextUsed: 41000,
        contextWindow: 200000,
        contextRemaining: 159000,
        contextPressure: 0.21,
        durationMs: 120000,
      },
      capabilities: ALL_TRUE_CAPS,
      reasoningEffort: "high",
      reasoningEffortLevels: ["low", "medium", "high"],
      supportsReasoning: true,
    },
    activityOpen: true,
  };
}

// ---------------------------------------------------------------------------
// Ask composer fixture: structured ask_user questions
// ---------------------------------------------------------------------------

export interface AskFixture {
  readonly route: "ask";
  readonly conversation: MobileConversation;
  readonly draft: string;
}

function askFixture(): AskFixture {
  const askBatch: AskBatch = {
    callId: "call-ask-1",
    questions: [
      {
        key: "call-ask-1:0",
        header: "Authentication Strategy",
        question:
          "Which authentication approach should I use for the new API endpoints?",
        options: [
          {
            label: "JWT with refresh tokens",
            detail: "Stateless, scales well, requires refresh token rotation",
            recommended: true,
          },
          {
            label: "Session-based with Redis",
            detail: "Stateful, simpler revocation, requires Redis dependency",
          },
          {
            label: "OAuth 2.0 with PKCE",
            detail:
              "Delegated, best for third-party clients, more complex setup",
          },
        ],
        multiSelect: false,
        why: "The choice affects the entire API security model and is hard to change later.",
        ifUnanswered: "I'll proceed with JWT as the default recommendation.",
      },
      {
        key: "call-ask-1:1",
        header: "Token Expiry",
        question: "What should the access token expiry be?",
        options: [
          { label: "15 minutes", detail: "Short-lived, better security" },
          {
            label: "1 hour",
            detail: "Balanced security and usability",
            recommended: true,
          },
          { label: "24 hours", detail: "Long-lived, fewer refreshes" },
        ],
        multiSelect: false,
        why: "Shorter expiry is safer but requires more refresh requests.",
      },
    ],
  };

  return {
    route: "ask",
    conversation: {
      id: "thread-ask-1",
      sessionId: "session-ask-1",
      name: "API authentication design",
      preview: "Which authentication approach should I use?",
      modelProvider: "anthropic",
      status: "ready",
      items: [
        {
          kind: "user",
          id: "u-ask-1",
          text: "I need to set up authentication for the new API. Can you help me decide on the approach?",
        },
        {
          kind: "assistant",
          id: "a-ask-1",
          markdown:
            "I can help you design the authentication system. Let me ask a few questions to guide the implementation.",
          streaming: false,
        },
        {
          kind: "question",
          id: "q-ask-1",
          batch: askBatch,
        },
      ],
      capabilities: ALL_TRUE_CAPS,
      queue: { depth: 0, preview: [] },
      usage: SAMPLE_USAGE,
      askPending: true,
    },
    draft: "",
  };
}

// ---------------------------------------------------------------------------
// Attachment strip fixture: conversation with image attachments
// ---------------------------------------------------------------------------

export interface AttachmentsFixture {
  readonly route: "attachments";
  readonly conversation: MobileConversation;
  readonly attachmentItems: MobileTimelineItem[];
}

function attachmentsFixture(): AttachmentsFixture {
  return {
    route: "attachments",
    conversation: {
      id: "thread-att-1",
      sessionId: "session-att-1",
      name: "Design review with screenshots",
      preview: "Please review these UI mockups",
      modelProvider: "anthropic",
      status: "ready",
      items: [
        {
          kind: "user",
          id: "u-att-1",
          text: "Here are the screenshots from the design review. Can you check the layout?",
        },
        {
          kind: "attachments",
          id: "att-strip-1",
          items: [
            {
              id: "img-1",
              src: "/images/screenshot-login.png",
              name: "login-screen.png",
              mediaType: "image/png",
            },
            {
              id: "img-2",
              src: "/images/screenshot-dashboard.png",
              name: "dashboard.png",
              mediaType: "image/png",
            },
            {
              id: "img-3",
              src: "/images/screenshot-settings.png",
              name: "settings-page.png",
              mediaType: "image/png",
            },
            {
              id: "img-4",
              src: "/images/screenshot-mobile.png",
              name: "mobile-view.png",
              mediaType: "image/jpeg",
            },
          ],
        },
        {
          kind: "assistant",
          id: "a-att-1",
          markdown:
            "I've reviewed the screenshots. The login screen looks good, but the dashboard could use better spacing.",
          streaming: false,
        },
      ],
      capabilities: ALL_TRUE_CAPS,
      queue: { depth: 0, preview: [] },
      usage: SAMPLE_USAGE,
      askPending: false,
    },
    attachmentItems: [
      {
        kind: "attachments",
        id: "att-strip-1",
        items: [
          {
            id: "img-1",
            src: "/images/screenshot-login.png",
            name: "login-screen.png",
            mediaType: "image/png",
          },
          {
            id: "img-2",
            src: "/images/screenshot-dashboard.png",
            name: "dashboard.png",
            mediaType: "image/png",
          },
          {
            id: "img-3",
            src: "/images/screenshot-settings.png",
            name: "settings-page.png",
            mediaType: "image/png",
          },
          {
            id: "img-4",
            src: "/images/screenshot-mobile.png",
            name: "mobile-view.png",
            mediaType: "image/jpeg",
          },
        ],
      },
    ],
  };
}

// ---------------------------------------------------------------------------
// New session fixture: new session form
// ---------------------------------------------------------------------------

export interface NewSessionFixture {
  readonly route: "new";
  readonly profiles: readonly ProfileRedacted[];
  readonly activeProfileId: string;
}

function newSessionFixture(): NewSessionFixture {
  return {
    route: "new",
    profiles: [
      { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
    ],
    activeProfileId: "p1",
  };
}

// ---------------------------------------------------------------------------
// Settings fixture: settings screen with profiles and preferences
// ---------------------------------------------------------------------------

export interface SettingsFixture {
  readonly route: "settings";
  readonly profiles: readonly ProfileRedacted[];
  readonly activeProfileId: string;
  readonly reachability: Record<string, Reachability>;
  readonly theme: "system" | "light" | "dark";
  readonly reducedMotion: boolean;
}

function settingsFixture(): SettingsFixture {
  return {
    route: "settings",
    profiles: [
      { id: "p1", name: "workstation", origin: "https://hub.internal:9000" },
      { id: "p2", name: "staging", origin: "http://10.0.0.5:8080" },
    ],
    activeProfileId: "p1",
    reachability: { p1: "unreachable", p2: "reconnecting" },
    theme: "system",
    reducedMotion: false,
  };
}

// ---------------------------------------------------------------------------
// Fixture union and entry point
// ---------------------------------------------------------------------------

export type ConversationFixtureData =
  | RosterFixture
  | ConversationFixture
  | ActivityFixture
  | AskFixture
  | AttachmentsFixture
  | NewSessionFixture
  | SettingsFixture;

export function createConversationFixture(
  route: ConversationFixtureRoute,
): ConversationFixtureData {
  switch (route) {
    case "roster":
      return rosterFixture();
    case "conversation":
      return conversationFixture();
    case "activity":
      return activityFixture();
    case "ask":
      return askFixture();
    case "attachments":
      return attachmentsFixture();
    case "new":
      return newSessionFixture();
    case "settings":
      return settingsFixture();
  }
}

/**
 * All fixture route names, in canonical order.
 */
export const CONVERSATION_FIXTURE_ROUTES: readonly ConversationFixtureRoute[] =
  [
    "roster",
    "conversation",
    "activity",
    "ask",
    "attachments",
    "new",
    "settings",
  ];

/**
 * A conversation fixture with restricted capabilities, for testing the
 * "Unavailable for this source" state in the activity sheet controls.
 */
export function restrictedCapabilitiesConversation(): MobileConversation {
  return {
    id: "thread-restricted-1",
    sessionId: "session-restricted-1",
    name: "Read-only session",
    preview: "This session does not support mutations",
    modelProvider: "anthropic",
    status: "ready",
    items: [
      {
        kind: "user",
        id: "u-r-1",
        text: "Show me the current configuration",
      },
      {
        kind: "assistant",
        id: "a-r-1",
        markdown:
          "Here is the current configuration. This is a read-only view.",
        streaming: false,
      },
    ],
    capabilities: ALL_FALSE_CAPS,
    queue: { depth: 0, preview: [] },
    usage: SAMPLE_USAGE,
    askPending: false,
  };
}
