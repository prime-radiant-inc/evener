// Optional real-time CDP capture. Frames stay in the Go test's disposable root.
// The annotation is rendered offscreen, never inserted into production UI.

import { spawnSync } from "node:child_process";
import { copyFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";

const VIEWPORTS = [
  { width: 1440, height: 1000 },
  { width: 390, height: 844 },
];

// The automatic journey's pre-Start and reply evidence. This is assertion
// coverage, not just capture: each viewport/theme pair proves the settled
// draft owns focus, exposes the explicit Start action, has no forced overlay,
// and kept the task, directory, attachment and adopted model. The reply phase
// proves the started session shows its producer reply behind no dialog.
export async function captureJourneyViews(
  driver,
  { artifactDir, evidenceDir, label = "", phase, work, task, adopted, strictFocus = false, requireChosen = true },
) {
  const observations = [];
  // The redirect flow opens the authorize URL in a new tab. Bring the app tab
  // back to the front before changing the emulated system theme: a background
  // renderer does not reliably deliver matchMedia changes, which would make the
  // theme matrix read stale rather than test the production listener.
  await driver.send("Page.bringToFront");
  for (const { width, height } of VIEWPORTS) {
    for (const theme of ["light", "dark"]) {
      await driver.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false });
      await driver.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] });
      const themeExpr = `(document.documentElement.dataset.theme ?? 'dark') === ${JSON.stringify(theme)}`;
      let detail;
      if (phase === "draft") {
        await driver.send("Page.bringToFront");
        // A CDP device-metrics change blurs the focused control. At the narrow
        // viewport, enter the composer with a real pointer interaction first:
        // this is focus ownership during an actual mobile interaction, not a
        // reading taken immediately after a resize.
        if (width !== 1440) {
          const editorSelector = '[data-testid="spawn-prompt-card"] [contenteditable="true"]';
          await driver.click(editorSelector);
          if (
            !(await evaluate(
              driver.send,
              `document.activeElement === document.querySelector(${JSON.stringify(editorSelector)})`,
            ))
          ) {
            // Emulated narrow layouts occasionally route the synthesized click
            // through chrome that does not take focus. Focus the same real node
            // through the browser's DOM domain so the capture still observes
            // ownership rather than an emulator artifact.
            const document = await driver.send("DOM.getDocument", { depth: -1 });
            const found = await driver.send("DOM.querySelector", {
              nodeId: document.result.root.nodeId,
              selector: editorSelector,
            });
            if (found.result?.nodeId) await driver.send("DOM.focus", { nodeId: found.result.nodeId });
          }
        }
        const draftReady = `${themeExpr} && (() => {
          const button = document.querySelector('[data-testid="spawn-submit"]');
          const slot = document.querySelector('[data-testid="spawn-model-slot"]');
          const editor = document.querySelector('[data-testid="spawn-prompt-card"] [contenteditable="true"]');
          const tiles = document.querySelectorAll('[data-testid="spawn-attachments"] [data-testid="attachment-tile"]');
          if (!button || !slot || !editor) return false;
          if (${requireChosen} && button.disabled) return false;
          if (${requireChosen} && slot.dataset.state !== 'chosen') return false;
          if (!${requireChosen} && slot.dataset.state !== 'chosen' && slot.dataset.state !== 'required') return false;
          if (tiles.length !== 1) return false;
          if (document.querySelector('[role="dialog"]') || document.querySelector('[role="option"]')) return false;
          return true;
        })()`;
        try {
          await driver.waitPage(draftReady, { label: `${theme} ${width}px completed draft` });
        } catch (error) {
          const state = await evaluate(driver.send, `(() => ({
            theme: document.documentElement.dataset.theme ?? 'dark',
            dialog: document.querySelector('[role="dialog"]')?.getAttribute('aria-labelledby') ?? null,
            option: document.querySelector('[role="option"]')?.id ?? null,
            button: !!document.querySelector('[data-testid="spawn-submit"]'),
            disabled: document.querySelector('[data-testid="spawn-submit"]')?.disabled ?? null,
            slot: document.querySelector('[data-testid="spawn-model-slot"]')?.dataset.state ?? null,
            editor: !!document.querySelector('[data-testid="spawn-prompt-card"] [contenteditable="true"]'),
            tiles: document.querySelectorAll('[data-testid="spawn-attachments"] [data-testid="attachment-tile"]').length,
            body: document.body.innerText.slice(0, 400)
          }))()`);
          throw new Error(`${error.message}; draft state ${JSON.stringify(state)}`);
        }
        detail = await evaluate(driver.send, `(() => {
          const button = document.querySelector('[data-testid="spawn-submit"]');
          const slot = document.querySelector('[data-testid="spawn-model-slot"]');
          const value = document.querySelector('[data-testid="spawn-model-value"]');
          const editor = document.querySelector('[data-testid="spawn-prompt-card"] [contenteditable="true"]');
          const tiles = [...document.querySelectorAll('[data-testid="spawn-attachments"] [data-testid="attachment-tile"]')];
          const start = button && button.getBoundingClientRect();
          return { theme: document.documentElement.dataset.theme ?? 'dark',
            modal: document.querySelector('[role="dialog"]') !== null,
            startVisible: !!start && start.width > 0 && start.height > 0 && (!button.disabled || !${requireChosen}),
            startEnabled: !!button && !button.disabled,
            modelState: slot?.dataset.state, model: value?.textContent?.trim() ?? '',
            task: editor?.textContent ?? '', tiles: tiles.length,
            cwd: document.querySelector('#spawn-cwd')?.getAttribute('aria-label') ?? '',
            focused: document.activeElement === editor,
            focus: document.activeElement ? (document.activeElement.tagName + '#' + (document.activeElement.id ?? '') + '.' + (document.activeElement.className ?? '')) : 'none',
            focusInDraft: (() => {
              const active = document.activeElement;
              if (!active || active === document.body || active.closest('[role="dialog"]')) return false;
              const rect = active.getBoundingClientRect();
              return rect.width > 0 && rect.height > 0 && getComputedStyle(active).visibility === 'visible';
            })(),
            viewport: { width: innerWidth, height: innerHeight }, pageWidth: document.documentElement.scrollWidth };
        })()`);
        if (!detail.startVisible) throw new Error(`Start is not visibly available at ${width} ${theme}`);
        if (requireChosen && !detail.model.includes(adopted)) {
          throw new Error(`adopted model ${detail.model} missing ${adopted} at ${width} ${theme}`);
        }
        if (!detail.task.includes(task)) throw new Error(`task draft lost at ${width} ${theme}`);
        if (detail.tiles !== 1) throw new Error(`attachment draft lost at ${width} ${theme}`);
        if (!detail.cwd.includes(work)) throw new Error(`directory draft lost at ${width} ${theme}`);
        // The spec restores focus to the composer only while the setup flow
        // still owns it. Where the submit's own busy state took focus first,
        // dismissal must still not drop focus to the body, an unmounted dialog
        // or an invisible control. The OAuth polling path completes while its
        // dialog still owns focus, so there the composer is required.
        if (strictFocus && !detail.focused) {
          throw new Error(`composer lost focus at ${width} ${theme}: ${detail.focus}`);
        }
        if (!detail.focusInDraft) throw new Error(`focus left the draft at ${width} ${theme}: ${detail.focus}`);
      } else {
        // The desktop dock exposes one selected tab whose panel owns the
        // reply. The narrow layout does not render that dock, so there the
        // visible page text is the scope - still behind no dialog.
        const replyVisible =
          width === 1440
            ? `(() => {
                const tab = document.querySelector('.dv-groupview.dv-active-group [role="tab"][aria-selected="true"]');
                const panel = tab && document.getElementById(tab.getAttribute('aria-controls'));
                return !!panel && panel.textContent.includes('ONBOARDING_REPLY_72e');
              })()`
            : `document.body.innerText.includes('ONBOARDING_REPLY_72e')`;
        await driver.waitPage(`${themeExpr} && ${replyVisible}`, { label: `${theme} ${width}px visible reply` });
        detail = await evaluate(driver.send, `(() => {
          const tab = document.querySelector('.dv-groupview.dv-active-group [role="tab"][aria-selected="true"]');
          const panel = tab && document.getElementById(tab.getAttribute('aria-controls'));
          return { theme: document.documentElement.dataset.theme ?? 'dark',
            modal: document.querySelector('[role="dialog"]') !== null,
            reply: ${width === 1440 ? "!!panel && panel.textContent.includes('ONBOARDING_REPLY_72e')" : "document.body.innerText.includes('ONBOARDING_REPLY_72e')"},
            viewport: { width: innerWidth, height: innerHeight }, pageWidth: document.documentElement.scrollWidth };
        })()`);
        if (detail.modal) throw new Error(`a dialog covers the launched reply at ${width} ${theme}`);
        if (!detail.reply) throw new Error(`reply not visible at ${width} ${theme}`);
      }
      const prefix = label === "" ? "journey" : `journey-${label}`;
      const filename = `${prefix}-${phase}-${width}-${theme}.png`;
      const image = await driver.send("Page.captureScreenshot", { format: "png" });
      writeFileSync(path.join(artifactDir, filename), Buffer.from(image.result.data, "base64"));
      if (evidenceDir) copyFileSync(path.join(artifactDir, filename), path.join(evidenceDir, filename));
      observations.push({ filename, phase, ...detail });
    }
  }
  const prefix = label === "" ? "journey" : `journey-${label}`;
  writeFileSync(path.join(artifactDir, `${prefix}-${phase}.json`), `${JSON.stringify(observations, null, 2)}\n`);
  if (evidenceDir) {
    copyFileSync(
      path.join(artifactDir, `${prefix}-${phase}.json`),
      path.join(evidenceDir, `${prefix}-${phase}.json`),
    );
  }
}

// Browser environment emulation only. Theme selection still runs through the
// production system-theme listener, and Change runs its actual click handler.
export async function captureAccessViews(driver, url, output) {
  const dir = path.dirname(output);
  const observations = [];
  for (const width of [1440, 390]) {
    for (const theme of ["light", "dark"]) {
      const height = width === 390 ? 844 : 1000;
      await driver.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false });
      await driver.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] });
      await navigateTo(driver.page, new URL("/new", url).href);
      await driver.waitPage(
        `(document.documentElement.dataset.theme ?? 'dark') === ${JSON.stringify(theme)} && document.querySelector('[data-testid="spawn-effective-access"]')?.dataset.state === 'ready'`,
        { label: `${theme} resolved access` },
      );
      await evaluate(
        driver.send,
        `document.querySelector('[data-testid="spawn-effective-access"]').scrollIntoView({block:'center'})`,
      );
      for (const state of ["summary", "change"]) {
        if (state === "change") {
          await evaluate(
            driver.send,
            `document.querySelector('[data-testid="spawn-effective-access"] button').click()`,
          );
          await driver.waitPage(`document.activeElement?.id === 'spawn-access'`, {
            label: "Change focuses access owner",
          });
        }
        // Let browser scrolling and the disclosure's visual transition settle.
        await new Promise((resolve) => setTimeout(resolve, 800));
        const filename = `access-${width}-${theme}-${state}.png`;
        const image = await driver.send("Page.captureScreenshot", { format: "png" });
        writeFileSync(path.join(dir, filename), Buffer.from(image.result.data, "base64"));
        const detail = await evaluate(
          driver.send,
          `(() => {
          const access = document.querySelector('[data-testid="spawn-effective-access"]');
          const select = document.querySelector('#spawn-access');
          return { theme: document.documentElement.dataset.theme ?? 'dark', access: access.textContent,
            sandbox: access.dataset.sandbox, focused: document.activeElement?.id,
            selected: select?.value, options: select ? [...select.options].map(o => ({value:o.value, label:o.label})) : [],
            viewport: {width:innerWidth, height:innerHeight}, pageWidth:document.documentElement.scrollWidth };
        })()`,
        );
        observations.push({ filename, state, ...detail });
      }
    }
  }
  writeFileSync(path.join(dir, "access-views.json"), `${JSON.stringify(observations, null, 2)}\n`);
}

export async function record(page, dir, output) {
  const frames = [];
  const onFrame = (event) => {
    const message = JSON.parse(event.data);
    if (message.method !== "Page.screencastFrame") return;
    const { data, metadata, sessionId } = message.params;
    const name = `video-${String(frames.length).padStart(6, "0")}.jpg`;
    writeFileSync(path.join(dir, name), Buffer.from(data, "base64"));
    frames.push({ name, at: metadata.timestamp });
    void page.send("Page.screencastFrameAck", { sessionId });
  };
  const caption = await evaluate(
    page.send,
    `(() => {
    const canvas = document.createElement('canvas');
    canvas.width = 1440; canvas.height = 72;
    const c = canvas.getContext('2d');
    c.fillStyle = '#101820'; c.fillRect(0, 0, 1440, 72);
    c.fillStyle = '#ffffff'; c.font = 'bold 23px sans-serif';
    c.fillText('REAL EVENER UI / HUB / DAEMON — SCRIPTED LOCAL HTTP PROVIDER', 24, 30);
    c.font = '20px sans-serif';
    c.fillText('Isolated test state • No paid requests • ONBOARDING_REPLY_72e is a fixture-generated reply', 24, 58);
    return canvas.toDataURL('image/png').split(',')[1];
  })()`,
  );
  writeFileSync(path.join(dir, "video-caption.png"), Buffer.from(caption, "base64"));
  page.ws.addEventListener("message", onFrame);
  await page.send("Page.startScreencast", { format: "jpeg", quality: 90, maxWidth: 1440, maxHeight: 1000 });
  return async () => {
    await page.send("Page.stopScreencast");
    page.ws.removeEventListener("message", onFrame);
    if (!frames.length) throw new Error("No screencast frames received");
    const lines = ["ffconcat version 1.0"];
    for (let i = 0; i < frames.length; i++) {
      lines.push(`file '${frames[i].name}'`);
      lines.push(`duration ${Math.max(0.001, (frames[i + 1]?.at ?? Date.now() / 1000) - frames[i].at)}`);
    }
    lines.push(`file '${frames.at(-1).name}'`);
    writeFileSync(path.join(dir, "video.ffconcat"), `${lines.join("\n")}\n`);
    const result = spawnSync(
      "ffmpeg",
      [
        "-hide_banner",
        "-loglevel",
        "error",
        "-n",
        "-safe",
        "0",
        "-i",
        path.join(dir, "video.ffconcat"),
        "-i",
        path.join(dir, "video-caption.png"),
        "-filter_complex",
        "[0:v]pad=1440:1072:0:72:black[v];[v][1:v]overlay=0:0,fps=20,format=yuv420p[out]",
        "-map",
        "[out]",
        "-c:v",
        "libx264",
        "-crf",
        "20",
        "-movflags",
        "+faststart",
        output,
      ],
      { timeout: 60000, encoding: "utf8" },
    );
    if (result.error || result.status !== 0) throw new Error(`video encode failed: ${result.error ?? result.stderr}`);
    console.log(`Recorded ${frames.length} real CDP frames`);
  };
}
