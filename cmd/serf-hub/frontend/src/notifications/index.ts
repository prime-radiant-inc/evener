// The notifications engine: owns document.title's attention count, the
// favicon badge, OS notifications, the alert sound, and single-tab (Web
// Locks) leader election, all driven off treeStore + connectionStore +
// prefsStore. AppShell calls initNotifications() once at module evaluation,
// beside initPrefs().
//
// T1 ships an idempotent no-op stub so the AppShell wiring lands once; T4
// fills the body (reading the pinned all-OFF prefs; never re-defaulting).
let initialized = false;

export function initNotifications(): void {
  if (initialized) return;
  initialized = true;
}
