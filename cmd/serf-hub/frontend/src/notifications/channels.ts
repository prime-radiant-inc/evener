// The two "loud" alert channels — an OS notification and a short tone —
// fired (per the engine's edge-fire gating) only for a qualifying transition,
// only when unfocused, only on the elected leader tab. Each also re-checks
// focus itself, matching the legacy's belt-and-suspenders gate
// (notifications.js:173,189). Every failure is silently swallowed: a browser
// that refuses either API must never break the app.
import { navigate } from "../shell/routing";
import type { AttentionEntry } from "./attention";

// OS notification. Requires the API, permission "granted", and an unfocused
// document (floor §3.4). Title "serf · <title||ref>"; construction failure
// swallowed. Click focuses the window (best-effort) and navigates to the
// session pane via SPA routing — no full reload, unlike the legacy's
// location.href assignment.
export function fireOsNotification(entry: AttentionEntry): void {
  const Ctor = window.Notification;
  if (typeof Ctor !== "function") return;
  if (Ctor.permission !== "granted") return;
  if (document.hasFocus?.()) return;
  let n: Notification;
  try {
    n = new Ctor(`serf · ${entry.title || entry.ref}`);
  } catch {
    return; // construction refused (e.g. permission race): no alert, no throw
  }
  n.onclick = () => {
    try {
      window.focus();
    } catch {
      // best-effort focus: a browser that blocks programmatic focus still navigates
    }
    navigate(`/s/${encodeURIComponent(entry.ref)}`);
  };
}

interface WebkitWindow {
  webkitAudioContext?: typeof AudioContext;
}

// Short alert tone: an 800 Hz oscillator at gain 0.1, stopped and the context
// closed after exactly 120 ms (floor §3.5). Re-checks focus first. A missing
// constructor or any construction/graph-wiring error is swallowed.
export function playTone(): void {
  if (document.hasFocus?.()) return;
  const Ctor = window.AudioContext ?? (window as unknown as WebkitWindow).webkitAudioContext;
  if (!Ctor) return;
  let ctx: AudioContext;
  try {
    ctx = new Ctor();
  } catch {
    return; // no AudioContext could be created
  }
  try {
    const osc = ctx.createOscillator();
    const gain = ctx.createGain ? ctx.createGain() : null;
    osc.frequency.value = 800;
    if (gain) {
      gain.gain.value = 0.1;
      osc.connect(gain);
      gain.connect(ctx.destination);
    } else {
      osc.connect(ctx.destination);
    }
    osc.start();
    setTimeout(() => {
      try {
        osc.stop();
      } catch {
        // best-effort stop
      }
      try {
        ctx.close?.();
      } catch {
        // best-effort close
      }
    }, 120);
  } catch {
    // graph wiring failed: no sound, no throw
  }
}
