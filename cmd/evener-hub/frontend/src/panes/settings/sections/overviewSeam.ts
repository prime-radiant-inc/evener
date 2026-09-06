// overviewSeam.ts is the thin indirection this task owns in place of
// stores/settingsOverview.ts, which T4 lands at the wave-7 merge (this
// stream's manifest explicitly excludes creating that file - see the
// wave-7 plan's T2 "BUILD ORDER" note). agents.tsx - the overview-fed section in this stream's scope - calls useSettingsOverview()
// from HERE, never a direct import of the not-yet-existing real store, so:
//   - this stream's own tests never import a module that doesn't exist yet
//   - the controller's merge step is a single-line repoint (this function's
//     body, or the one call site) once stores/settingsOverview.ts exists,
//     exporting useSettingsOverviewStore with the exact SettingsOverviewStore
//     shape pinned below
//   - the consumer additionally accepts an optional `useOverview` prop
//     (defaulting to this function) so their own tests inject a fixture
//     store without needing this module at all
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";

export interface SettingsOverviewStore {
  data: SettingsOverviewResponse | null;
  loading: boolean;
  error: string | null;
  fetch(): Promise<void>;
}

const PLACEHOLDER_STORE: SettingsOverviewStore = {
  data: null,
  loading: false,
  error: null,
  fetch: async () => {},
};

export function useSettingsOverview(): SettingsOverviewStore {
  return PLACEHOLDER_STORE;
}
