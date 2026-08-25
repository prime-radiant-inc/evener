import type { PrototypeFixture, SessionRecord, TranscriptItem } from "./model";

const sessions = [
  {
    id: "session-mobile-release",
    title: "Mobile release checklist",
    project: "Evener",
    state: "needs-answer",
    summary: "Answer 2 questions",
    updatedLabel: "now",
  },
  {
    id: "session-pairing-review",
    title: "Pairing security review",
    project: "prime-radiant",
    state: "needs-permission",
    summary: "Permission needed",
    updatedLabel: "3m",
  },
  {
    id: "session-native-client",
    title: "Native mobile client",
    project: "Evener",
    state: "running",
    summary: "3 subagents · Implementing voice",
    updatedLabel: "12m",
  },
  {
    id: "session-roster-latency",
    title: "Hub roster latency",
    project: "Evener",
    state: "running",
    summary: "Testing source timeout",
    updatedLabel: "18m",
  },
  {
    id: "session-pairing-pr",
    title: "Pairing endpoint review",
    project: "prime-radiant",
    state: "completed",
    summary: "Completed · 14 changes",
    updatedLabel: "1h",
  },
] as const satisfies readonly SessionRecord[];

const longDisclosure =
  "The voice state reducer keeps each transition explicit and presentation-neutral. " +
  "It accepts ready, listening, processing, speaking, interrupted, denied, and error events without consulting a clock. " +
  "Each event produces a new record, preserves the previous caption for audit display, and bounds the visible level between zero and one hundred. " +
  "The renderer may disclose the full transition table, but the fixture remains deterministic and contains no device capability calls. " +
  "A denied transition offers a local retry explanation; an interrupted transition returns to ready; an error transition remains recoverable.";

const longToolOutput = `type VoiceState = idle | ready | listening | processing | speaking | interrupted | denied | error
transition(ready, begin) -> listening
transition(listening, finish) -> processing
transition(processing, respond) -> speaking
transition(speaking, interrupt) -> interrupted
transition(interrupted, reset) -> ready
transition(ready, refuse) -> denied
transition(processing, fail) -> error
verification: every fictional transition is explicit, deterministic, locally recoverable, and independent of clocks or device services`;

const transcript = [
  {
    id: "item-user-goal",
    sessionId: "session-native-client",
    kind: "user",
    body: "Prepare the mobile interaction states and keep the prototype deterministic.",
  },
  {
    id: "item-assistant-plan",
    sessionId: "session-native-client",
    kind: "assistant",
    body: "I will define the fixture states first, then compare the same content across each concept.",
  },
  {
    id: "item-tool-inspect",
    sessionId: "session-native-client",
    kind: "tool",
    label: "Inspect fixture schema",
    status: "completed",
    arguments: "scope=voice-state records=8",
    output: longToolOutput,
  },
  {
    id: "item-tool-verify",
    sessionId: "session-native-client",
    kind: "tool",
    label: "Verify transition table",
    status: "failed",
    arguments: "case=denied recovery=local",
    output:
      "One draft omitted the interrupted transition. The canonical table remains unchanged pending correction.",
  },
  {
    id: "item-error-transition",
    sessionId: "session-native-client",
    kind: "error",
    title: "Transition draft incomplete",
    detail:
      "The interrupted state was absent. Retry with the complete fictional state table.",
  },
  {
    id: "item-attachment-sketch",
    sessionId: "session-native-client",
    kind: "attachment",
    name: "voice-flow-sketch.txt",
    mediaType: "text/plain",
    description: "A fictional text sketch of the voice interaction sequence.",
  },
  {
    id: "item-assistant-disclosure",
    sessionId: "session-native-client",
    kind: "assistant",
    body: longDisclosure,
  },
] as const satisfies readonly TranscriptItem[];

function deepFreeze<T>(value: T): T {
  if (value !== null && typeof value === "object" && !Object.isFrozen(value)) {
    for (const child of Object.values(value)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
}

export const canonicalFixture: PrototypeFixture = deepFreeze({
  version: 1,
  sessions,
  transcript,
  questions: [
    {
      id: "question-release-focus",
      prompt: "Which release surface should be reviewed first?",
      mode: "single",
      options: [
        {
          id: "option-navigation",
          label: "Navigation",
          detail: "Review route and back behavior.",
          recommended: true,
        },
        {
          id: "option-typography",
          label: "Typography",
          detail: "Review scale and disclosure density.",
          recommended: false,
        },
      ],
      allowNote: true,
      allowFallback: true,
      allowDecide: true,
      allowSkip: false,
    },
    {
      id: "question-release-checks",
      prompt: "Which checks belong in the comparison pass?",
      mode: "multiple",
      options: [
        {
          id: "option-offline",
          label: "Offline",
          detail: "Compare stale-content treatment.",
          recommended: true,
        },
        {
          id: "option-accessibility",
          label: "Accessibility",
          detail: "Compare large text behavior.",
          recommended: true,
        },
        {
          id: "option-voice",
          label: "Voice",
          detail: "Compare the fictional voice sequence.",
          recommended: false,
        },
      ],
      allowNote: true,
      allowFallback: true,
      allowDecide: true,
      allowSkip: true,
    },
  ],
  work: [
    {
      id: "work-task-shell",
      sessionId: "session-native-client",
      parentId: null,
      kind: "task",
      title: "Build shell states",
      state: "running",
      phase: "Implementing",
      elapsedLabel: "8m",
      output: "Navigation and disclosure states are in progress.",
    },
    {
      id: "work-subagent-navigation",
      sessionId: "session-native-client",
      parentId: "work-task-shell",
      kind: "subagent",
      title: "Review navigation",
      state: "waiting",
      phase: "Reviewing",
      elapsedLabel: "4m",
      output: "Waiting for the route comparison pass.",
    },
    {
      id: "work-job-route-matrix",
      sessionId: "session-native-client",
      parentId: "work-subagent-navigation",
      kind: "job",
      title: "Check route matrix",
      state: "completed",
      phase: "Complete",
      elapsedLabel: "2m",
      output: "All fictional route cases resolved.",
    },
    {
      id: "work-task-voice",
      sessionId: "session-native-client",
      parentId: null,
      kind: "task",
      title: "Model voice sequence",
      state: "running",
      phase: "Implementing",
      elapsedLabel: "6m",
      output: "Eight deterministic voice states are represented.",
    },
    {
      id: "work-subagent-voice",
      sessionId: "session-native-client",
      parentId: "work-task-voice",
      kind: "subagent",
      title: "Review interruption states",
      state: "failed",
      phase: "Needs retry",
      elapsedLabel: "3m",
      output: "The first draft omitted one state.",
    },
    {
      id: "work-job-voice-table",
      sessionId: "session-native-client",
      parentId: "work-subagent-voice",
      kind: "job",
      title: "Verify voice table",
      state: "failed",
      phase: "Stopped",
      elapsedLabel: "1m",
      output: "Verification found the incomplete draft.",
    },
    {
      id: "work-task-copy",
      sessionId: "session-native-client",
      parentId: null,
      kind: "task",
      title: "Polish neutral copy",
      state: "completed",
      phase: "Complete",
      elapsedLabel: "5m",
      output: "Presentation-neutral copy is ready.",
    },
  ],
  search: [
    {
      id: "search-session",
      sessionId: "session-native-client",
      itemId: null,
      kind: "session",
      title: "Native mobile client",
      body: "Active concept comparison session.",
    },
    {
      id: "search-project",
      sessionId: "session-pairing-review",
      itemId: null,
      kind: "project",
      title: "prime-radiant",
      body: "Fictional project result for pairing review.",
    },
    {
      id: "search-transcript",
      sessionId: "session-native-client",
      itemId: "item-assistant-plan",
      kind: "transcript",
      title: "Fixture-first plan",
      body: "Define shared fixture states before rendering.",
    },
    {
      id: "search-tool",
      sessionId: "session-native-client",
      itemId: "item-tool-inspect",
      kind: "tool",
      title: "Inspect fixture schema",
      body: "Eight voice states were found.",
    },
    {
      id: "search-task",
      sessionId: "session-native-client",
      itemId: "item-tool-verify",
      kind: "task",
      title: "Verify transition table",
      body: "The incomplete draft needs correction.",
    },
  ],
  recentProjects: [
    {
      id: "project-aurora",
      label: "Aurora workspace",
      path: "/workspace/aurora",
    },
    {
      id: "project-harbor",
      label: "Harbor workspace",
      path: "/workspace/harbor",
    },
    {
      id: "project-fixture-path",
      label: "Typed fixture path",
      path: "/workspace/aurora/typed-fixture",
    },
  ],
  models: [
    {
      id: "model-northstar",
      provider: "Fictional Labs",
      model: "northstar-2",
      label: "Northstar 2",
    },
    {
      id: "model-lantern",
      provider: "Imaginary Systems",
      model: "lantern-small",
      label: "Lantern Small",
    },
    {
      id: "model-tidepool",
      provider: "Fictional Labs",
      model: "tidepool-reasoning",
      label: "Tidepool Reasoning",
    },
  ],
  efforts: ["low", "medium", "high"],
  voiceSteps: [
    { id: "voice-idle", state: "idle", caption: "Voice is idle", level: 0 },
    { id: "voice-ready", state: "ready", caption: "Ready to begin", level: 0 },
    {
      id: "voice-listening",
      state: "listening",
      caption: "Listening",
      level: 62,
    },
    {
      id: "voice-processing",
      state: "processing",
      caption: "Processing",
      level: 18,
    },
    { id: "voice-speaking", state: "speaking", caption: "Speaking", level: 74 },
    {
      id: "voice-interrupted",
      state: "interrupted",
      caption: "Interrupted",
      level: 12,
    },
    {
      id: "voice-denied",
      state: "denied",
      caption: "Permission denied",
      level: 0,
    },
    {
      id: "voice-error",
      state: "error",
      caption: "Voice unavailable",
      level: 0,
    },
  ],
  hubs: [
    {
      id: "hub-lantern",
      name: "Lantern Hub",
      context: "Fictional studio roster",
      state: "connected",
    },
    {
      id: "hub-tidepool",
      name: "Tidepool Hub",
      context: "Imaginary review roster",
      state: "offline",
    },
  ],
  usage: {
    tokens: 18420,
    costLabel: "¤0.42 fictional",
    durationLabel: "14m 08s",
    contextPercent: 38,
  },
});
