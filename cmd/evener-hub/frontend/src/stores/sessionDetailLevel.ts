// The detail level chosen for one session in this browser. It sits above the
// browser's default view and the hub default: a session with a level shows
// that level's content and keeps the default's advanced settings, and a
// session without one shows the default unchanged.
//
// The choice is kept per session ref in localStorage, so it belongs to this
// browser, and it is the same choice at desktop and phone widths: it names
// the session, not a layout. Other tabs read it when they next load.
import {
  accessibleConfigSummary,
  type ContentLevel,
  configAtLevel,
  type DetailLevelStorage,
  SessionDetailLevels,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { useSyncExternalStore } from "react";
import { transitionTranscriptViews } from "../panes/session/transcript/flow/transcriptViewRegistry";

export const SESSION_DETAIL_LEVEL_KEY = "evener.transcript-display.session-level.v1";

// localStorage can be absent or refuse access (private mode, a full quota):
// a read then finds nothing chosen, and a write is the store's to survive.
const browserStorage: DetailLevelStorage = {
  getItemSync: (key) => {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  },
  setItemSync: (key, value) => localStorage.setItem(key, value),
};

let levels = new SessionDetailLevels(browserStorage, SESSION_DETAIL_LEVEL_KEY);

export function sessionDetailLevel(ref: string): ContentLevel | null {
  return levels.get(ref);
}

export function useSessionDetailLevel(ref: string | undefined): ContentLevel | null {
  return useSyncExternalStore(
    (listener) => levels.subscribe(listener),
    () => (ref === undefined ? null : levels.get(ref)),
  );
}

/** What a session shows: its own level over the default's advanced settings,
 * or the default when it has no level of its own. */
export function configForSession(
  level: ContentLevel | null,
  base: TranscriptDisplayConfigV1,
): TranscriptDisplayConfigV1 {
  return level === null ? base : configAtLevel(level, base);
}

/** Chooses a level for one session, or returns it to the default with null.
 * `base` is the default the session would otherwise show. The change goes
 * through the transcript view transition, so every mounted reader keeps its
 * reading position across the re-projection and hears what it now shows. */
export function setSessionDetailLevel(ref: string, level: ContentLevel | null, base: TranscriptDisplayConfigV1): void {
  if (levels.get(ref) === level) return;
  transitionTranscriptViews(
    () => {
      if (level === null) levels.clear(ref);
      else levels.set(ref, level);
    },
    accessibleConfigSummary(configForSession(level, base)),
  );
}

export function resetSessionDetailLevelsForTests(): void {
  levels = new SessionDetailLevels(browserStorage, SESSION_DETAIL_LEVEL_KEY);
}
