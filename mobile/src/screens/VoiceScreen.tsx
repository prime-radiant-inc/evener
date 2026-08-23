/**
 * VoiceScreen — focused, full-screen duplex voice experience.
 *
 * Replaces the typed composer while active. Owns a VoiceCoordinator for the
 * active conversation ref: starts voice on mount (after permission check),
 * consumes conversation deltas to drive synthesis, and ends native voice on
 * unmount or when the user taps End/Keyboard.
 *
 * Layout (top-to-bottom):
 *   - Compact session title + connection status mark (always visible)
 *   - Voice status (listening/speaking/interrupted/error)
 *   - Level waveform (decorative)
 *   - Live captions (throttled live region) + latest assistant sentence
 *   - Ask-mode disabled reason (when conversation is in AskPending)
 *   - End / Keyboard controls (44px)
 *
 * Permission rationale: when voice permissions are not granted, shows a
 * rationale screen with an "Open Settings" action instead of the voice UI.
 *
 * Focus return: on end, focus is returned to the composer via the supplied
 * composerFocusRef. Haptic feedback fires on status transitions.
 */
import { type JSX, useCallback, useEffect, useRef, useState } from "react";
import type { StoreApi, UseBoundStore } from "zustand";
import {
  type CaptionTone,
  VoiceCaptions,
} from "../components/voice/VoiceCaptions";
import { VoiceControls } from "../components/voice/VoiceControls";
import { VoiceLevel } from "../components/voice/VoiceLevel";
import { VoiceStatus } from "../components/voice/VoiceStatus";
import type { NativeBridge } from "../native/client";
import type { ConversationState } from "../state/conversation";
import type { VoiceState } from "../state/voice";
import type { StatusMark as StatusMarkType } from "../ui/StatusMark";
import { StatusMark } from "../ui/StatusMark";
import {
  type VoiceClock,
  VoiceCoordinator,
  type VoiceCoordinatorCallbacks,
  type VoiceRef,
} from "../voice/coordinator";
import styles from "./VoiceScreen.module.css";

export interface VoiceScreenProps {
  /** The conversation store hook (Zustand) — provides the active ref + title. */
  readonly conversationStore: UseBoundStore<StoreApi<ConversationState>>;
  /** The voice store hook (Zustand) — status/captions/level/sentence. */
  readonly voiceStore: UseBoundStore<StoreApi<VoiceState>>;
  /** The native bridge — used for permission checks and haptics. */
  readonly bridge: NativeBridge;
  /** Injected clock for deterministic coordinator timing. Defaults to wall clock. */
  readonly clock?: VoiceClock;
  /** Ref to the typed composer input; focus returns here when voice ends. */
  readonly composerFocusRef: { current: HTMLElement | null };
  /** Called after End to dismiss the voice screen. */
  readonly onEnd: () => void;
  /** Called after Keyboard to dismiss the voice screen (voice already ended). */
  readonly onKeyboard: () => void;
  /** Connection status mark shown beside the title. */
  readonly connectionStatus: StatusMarkProps["status"];
}

type StatusMarkProps = {
  status: Parameters<typeof StatusMarkType>[0]["status"];
};

const ASSISTANT_SENTENCE_LABEL = "Assistant";

export function VoiceScreen({
  conversationStore,
  voiceStore,
  bridge,
  clock,
  composerFocusRef,
  onEnd,
  onKeyboard,
  connectionStatus,
}: VoiceScreenProps): JSX.Element {
  const conversation = conversationStore((s) => s.conversation);
  const ref = conversationStore((s) => s.ref);

  const status = voiceStore((s) => s.status);
  const caption = voiceStore((s) => s.captions);
  const level = voiceStore((s) => s.level);
  const latestAssistantSentence = voiceStore((s) => s.latestAssistantSentence);
  const error = voiceStore((s) => s.error);
  const hasError = voiceStore((s) => s.hasError);

  const [permissionsGranted, setPermissionsGranted] = useState<boolean | null>(
    null,
  );
  const [ending, setEnding] = useState(false);

  // Coordinator is created once; refs hold the latest store mutators so the
  // callbacks always write to the current store without re-creating the
  // coordinator on every render.
  const coordinatorRef = useRef<VoiceCoordinator | null>(null);
  const storeRef = useRef(voiceStore);
  storeRef.current = voiceStore;
  const bridgeRef = useRef(bridge);
  bridgeRef.current = bridge;

  // Track the previous status for haptic-on-transition.
  const prevStatusRef = useRef<string>(status);

  // --- Coordinator callbacks write to the voice store. ----------------------
  const callbacks = useRef<VoiceCoordinatorCallbacks>({
    onListening: (active: boolean) =>
      storeRef.current.getState().setListening(active),
    onSpeaking: (active: boolean) =>
      storeRef.current.getState().setSpeaking(active),
    onInterrupted: (interrupted: boolean) =>
      storeRef.current.getState().setInterrupted(interrupted),
    onError: (message: string) => storeRef.current.getState().setError(message),
    onCaption: (text: string) => storeRef.current.getState().setCaption(text),
    onLevel: (lv: number) => storeRef.current.getState().setLevel(lv),
    onAssistantSentence: (text: string) =>
      storeRef.current.getState().setAssistantSentence(text),
    onVoiceSessionId: (id: string | null) =>
      storeRef.current.getState().setVoiceSessionId(id),
    onIdleStartReport: () => {},
  }).current;

  // --- Start voice on mount. -------------------------------------------------
  // biome-ignore lint/correctness/useExhaustiveDependencies: mount-only start; the parent remounts VoiceScreen with a fresh key when the conversation ref changes, so the effect does not need to re-run on ref/callbacks/clock.
  useEffect(() => {
    let cancelled = false;
    const coordinator = new VoiceCoordinator({
      bridge: bridgeRef.current,
      clock: clock ?? wallClock,
      callbacks,
    });
    coordinatorRef.current = coordinator;

    async function start() {
      try {
        const granted = await bridgeRef.current.voicePermissions();
        if (cancelled) return;
        setPermissionsGranted(granted);
        if (!granted) return;
        if (ref === null) return;
        const voiceRef: VoiceRef = { ref, threadId: conversation?.id ?? ref };
        await coordinator.start(voiceRef);
      } catch {
        if (!cancelled) setPermissionsGranted(false);
      }
    }

    void start();
    return () => {
      cancelled = true;
      coordinator.end();
      coordinatorRef.current = null;
    };
    // We intentionally start once for the active conversation ref.
  }, []);

  // --- Feed conversation deltas to the coordinator. -------------------------
  useEffect(() => {
    if (conversation !== null) {
      coordinatorRef.current?.consumeConversation(conversation);
    }
  }, [conversation]);

  // --- Haptics on status transitions. ---------------------------------------
  useEffect(() => {
    const prev = prevStatusRef.current;
    if (prev !== status) {
      prevStatusRef.current = status;
      const kind = hapticForTransition(prev, status);
      if (kind !== null) {
        void bridgeRef.current.hapticPerform(kind).catch(() => {});
      }
    }
  }, [status]);

  // --- End voice and return focus. ------------------------------------------
  const endVoice = useCallback(() => {
    if (ending) return;
    setEnding(true);
    coordinatorRef.current?.end();
    storeRef.current.getState().reset();
    // Return focus to the typed composer.
    const target = composerFocusRef.current;
    if (target !== null) {
      target.focus();
    }
    onEnd();
  }, [ending, composerFocusRef, onEnd]);

  // --- Keyboard fallback: end native voice BEFORE navigating. ---------------
  const switchToKeyboard = useCallback(() => {
    if (ending) return;
    setEnding(true);
    // End native voice first, then navigate. The onKeyboard handler navigates
    // to typed input; voice is fully stopped before it runs.
    coordinatorRef.current?.end();
    storeRef.current.getState().reset();
    const target = composerFocusRef.current;
    if (target !== null) {
      target.focus();
    }
    onKeyboard();
  }, [ending, composerFocusRef, onKeyboard]);

  // --- Permission rationale --------------------------------------------------
  if (permissionsGranted === false) {
    return (
      <PermissionRationale
        onOpenSettings={() => {
          void bridgeRef.current
            .requestPermission("microphone")
            .catch(() => {});
        }}
        error={error ?? undefined}
      />
    );
  }
  if (permissionsGranted === null) {
    return (
      <main className={styles.screen} data-testid="voice-screen">
        <header className={styles.header}>
          <SessionTitle
            title={conversation?.name ?? "Conversation"}
            status={connectionStatus}
          />
        </header>
        <div className={styles.body}>
          <div className={styles.placeholder} role="status">
            Checking voice permissions…
          </div>
        </div>
      </main>
    );
  }

  // --- Error state with settings action (permission errors) -----------------
  const isPermissionError =
    hasError && error !== null && /permission|denied/i.test(error);

  // --- Ask-mode disabled reason ---------------------------------------------
  const askPending = conversation?.askPending ?? false;
  const askDisabledReason = askPending
    ? "Voice input is disabled while questions are pending. Answer the questions or dismiss them to resume voice."
    : null;

  // --- Caption tone: final when speaking/idle-with-text, partial when listening.
  const captionTone: CaptionTone =
    status === "listening" ? "partial" : caption.length > 0 ? "final" : "idle";

  const active = status === "listening" || status === "speaking";

  return (
    <main
      className={styles.screen}
      data-testid="voice-screen"
      data-status={status}
    >
      <header className={styles.header}>
        <SessionTitle
          title={conversation?.name ?? "Conversation"}
          status={connectionStatus}
        />
        <VoiceStatus status={status} />
      </header>

      <div className={styles.body}>
        <div className={styles.level}>
          <VoiceLevel level={level} reducedMotion={false} active={active} />
        </div>

        <section className={styles.captions}>
          <VoiceCaptions text={caption} tone={captionTone} />
        </section>

        {latestAssistantSentence.length > 0 ? (
          <section
            className={styles.assistant}
            aria-label={ASSISTANT_SENTENCE_LABEL}
          >
            <span className={styles.assistantLabel} aria-hidden="true">
              {ASSISTANT_SENTENCE_LABEL}
            </span>
            <p
              className={styles.assistantText}
              data-testid="voice-assistant-sentence"
            >
              {latestAssistantSentence}
            </p>
          </section>
        ) : null}

        {askDisabledReason !== null ? (
          <section
            className={styles.askDisabled}
            role="alert"
            data-testid="voice-ask-disabled"
          >
            <span className={styles.askDisabledGlyph} aria-hidden="true">
              ⏸
            </span>
            <span className={styles.askDisabledText}>{askDisabledReason}</span>
          </section>
        ) : null}

        {hasError && !isPermissionError ? (
          <section
            className={styles.error}
            role="alert"
            data-testid="voice-error"
          >
            <span className={styles.errorText}>{error ?? "Voice error"}</span>
            <button
              type="button"
              className={styles.errorAction}
              onClick={endVoice}
            >
              Back to keyboard
            </button>
          </section>
        ) : null}

        {isPermissionError ? (
          <section
            className={styles.error}
            role="alert"
            data-testid="voice-error"
          >
            <span className={styles.errorText}>{error}</span>
            <button
              type="button"
              className={styles.errorAction}
              data-testid="voice-open-settings"
              onClick={() => {
                void bridgeRef.current
                  .requestPermission("microphone")
                  .catch(() => {});
              }}
            >
              Open Settings
            </button>
          </section>
        ) : null}
      </div>

      <footer className={styles.footer}>
        <VoiceControls
          onEnd={endVoice}
          onKeyboard={switchToKeyboard}
          disabled={ending}
        />
      </footer>
    </main>
  );
}

// ---------------------------------------------------------------------------
// Permission rationale
// ---------------------------------------------------------------------------

function PermissionRationale({
  onOpenSettings,
  error,
}: {
  readonly onOpenSettings: () => void;
  readonly error?: string;
}): JSX.Element {
  return (
    <main
      className={styles.screen}
      data-testid="voice-screen"
      data-status="permission-denied"
    >
      <header className={styles.header}>
        <h2 className={styles.rationaleTitle}>Voice needs microphone access</h2>
      </header>
      <div className={styles.body}>
        <div className={styles.rationale} role="status">
          <p className={styles.rationaleText}>
            Voice mode uses the microphone to listen and transcribe your speech.
            Grant microphone permission in Settings to start talking.
          </p>
          {error ? (
            <p className={styles.rationaleError} role="alert">
              {error}
            </p>
          ) : null}
        </div>
      </div>
      <footer className={styles.footer}>
        <button
          type="button"
          className={styles.openSettings}
          data-testid="voice-open-settings"
          onClick={onOpenSettings}
        >
          Open Settings
        </button>
      </footer>
    </main>
  );
}

// ---------------------------------------------------------------------------
// Session title + connection mark
// ---------------------------------------------------------------------------

function SessionTitle({
  title,
  status,
}: {
  readonly title: string;
  readonly status: StatusMarkProps["status"];
}): JSX.Element {
  return (
    <div className={styles.sessionTitle} data-testid="voice-session-title">
      <span className={styles.sessionTitleText}>{title}</span>
      <StatusMark status={status} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Haptic selection for status transitions
// ---------------------------------------------------------------------------

function hapticForTransition(
  prev: string,
  next: string,
):
  | "selection"
  | "impactLight"
  | "impactMedium"
  | "impactHeavy"
  | "notificationSuccess"
  | "notificationWarning"
  | "notificationError"
  | null {
  // No haptic on the initial mount transition (idle → listening is handled
  // when the coordinator sets listening).
  if (prev === next) return null;
  if (next === "error") return "notificationError";
  if (next === "interrupted") return "impactMedium";
  if (next === "speaking") return "selection";
  if (next === "listening") return "impactLight";
  if (next === "idle" && prev !== "idle") return "notificationSuccess";
  return null;
}

const wallClock: VoiceClock = { now: () => Date.now() };
