import { buildEntityView, type EntityView } from "@evener/appwire-client";
import { EntityRef } from "../../panes/session/transcript/EntityRef";
import { HoverCard } from "../../widgets/hovercard";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./tooltip.module.css";

// The entity-card demo renders EntityRef itself with synthetic views, not a
// hand-rolled lookalike, so this section can never drift from the production
// card. triggerOnly keeps the demo a pure hover target - no Open button
// wiring a click into the workspace store from inside the gallery.

function jobDemoView(): EntityView {
  const tree = {
    revision: 1,
    root: {
      kind: "session" as const,
      sessionId: "gallery",
      ref: "local:gallery",
      label: "root",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 1, complete: true },
      entries: [
        {
          kind: "shell" as const,
          job: {
            ownerSessionId: "gallery",
            ownerRef: "local:gallery",
            type: "shell",
            status: "completed",
            outcome: "success",
            terminal: true,
            background: false,
            hasOutput: true,
            description: "Tests passed in 14 seconds",
            command: "npm test -- src/foo",
            startedAt: "2026-09-13T20:00:00Z",
            endedAt: "2026-09-13T20:00:14Z",
            exitCode: 0,
            outputBytes: 6339,
            jobId: "job_gallery_demo",
          },
        },
      ],
      branch: {},
    },
  };
  const view = buildEntityView({ sessionRef: "local:gallery", tree, turns: [], stale: false, ended: false }).get(
    "job_gallery_demo",
  );
  if (!view) throw new Error("gallery: job fixture did not resolve");
  return view;
}

function delegateDemoView(): EntityView {
  const view = buildEntityView({
    sessionRef: "local:gallery",
    delegates: [
      {
        ownerSessionId: "gallery",
        rootSessionId: "gallery",
        childSessionId: "gallery-child",
        transcriptRef: "local:gallery-child",
        type: "delegate",
        lifecycle: "running",
        phase: "running",
        status: "running",
        resumable: true,
        needsAttention: false,
        projectionRevision: 1,
        task: "Review the hover card pass",
        agentType: "reviewer",
        resolvedModel: "gpt-test",
        runningForMs: 42_000,
        usage: { inputTokens: 1_200, outputTokens: 300 },
        delegateId: "dlg_gallery_demo",
      },
    ],
    turns: [],
    stale: false,
    ended: false,
  }).get("dlg_gallery_demo");
  if (!view) throw new Error("gallery: delegate fixture did not resolve");
  return view;
}

function HoverCardDemo() {
  return (
    <div className={styles.demo}>
      <p className={styles.hint}>
        Hover or focus a trigger. The bare widget takes any content; the entity references render the real production
        card.
      </p>
      <p>
        <HoverCard label={<div>Tests passed in 14 seconds</div>}>
          <button type="button">job_01HZX7P9</button>
        </HoverCard>{" "}
        · <EntityRef view={jobDemoView()} id="job_gallery_demo" triggerOnly /> ·{" "}
        <EntityRef view={delegateDemoView()} id="dlg_gallery_demo" triggerOnly />
      </p>
    </div>
  );
}

export default function HoverCardGallerySection() {
  return (
    <section>
      <h2>HoverCard</h2>
      <ThemeFlip>
        <HoverCardDemo />
      </ThemeFlip>
    </section>
  );
}
