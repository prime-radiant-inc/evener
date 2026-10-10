// notificationWireFixtures reads the daemon's own recorded notification frames.
//
// A delegate's report, its quiet watchdog, and a background job's completion
// reach a session as steering items whose text carries <delegate-notification>
// or <job-notification> markup. A parser tested against hand-built frames pins
// a shape the daemon may never send: the delegate parser once read
// status/description/excerpt markup that no producer writes, so every real
// subagent report rendered as an empty card while its suite stayed green.
//
// agent's TestSteeringNotificationWireFixtures produces the fixture by running
// the real frame producers and projecting each steering turn through
// apptranscript, and re-verifies it on every Go test run; regenerate it with
// `make fuzz-goldens`.
//
// The fixture is loaded through a `?raw` import, as hubWireFixtures loads its
// own: an import resolves against this file, where a filesystem read would
// resolve against whichever working directory the test runner uses.

import steeringWire from "../../../agent/testdata/notificationwire/steering.json?raw";
import type { ThreadItem } from "../types.gen";

const FIXTURE_PATH = "agent/testdata/notificationwire/steering.json";

interface NotificationWireFixture {
  case: string;
  note: string;
  item: ThreadItem;
}

/** The recorded case names, in the order the fixture lists them. */
export type NotificationWireCase =
  | "delegate-reported"
  | "delegate-failed-unnamed"
  | "delegate-stopped"
  | "delegate-stopped-by-parent"
  | "delegate-stopped-by-parent-mid-run"
  | "delegate-exhausted"
  | "delegate-quiet"
  | "delegate-update"
  | "job-shell-completed"
  | "job-shell-failed"
  | "job-shell-killed"
  | "job-shell-cancelled"
  | "job-shell-attention"
  | "job-pair"
  | "job-watch-send";

/** notificationWireItem returns the steering item the daemon sends for one recorded case. */
export function notificationWireItem(name: NotificationWireCase): ThreadItem {
  const records = JSON.parse(steeringWire) as NotificationWireFixture[];
  const record = records.find((rec) => rec.case === name);
  if (!record) throw new Error(`no ${name} case in ${FIXTURE_PATH}`);
  return record.item;
}

/** notificationWireItems returns every recorded steering item, in fixture order. */
export function notificationWireItems(): ThreadItem[] {
  return (JSON.parse(steeringWire) as NotificationWireFixture[]).map((rec) => rec.item);
}
