// Optional real-time CDP capture. Frames stay in the Go test's disposable root.
// The annotation is rendered offscreen, never inserted into production UI.

import { spawnSync } from "node:child_process";
import { copyFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";

// Acquisition evidence is taken in the layout the journey actually ran in.
// Nothing here resizes, clicks, focuses or otherwise touches the page: the
// production flow must have restored focus and settled the draft on its own.
export async function captureAcquisition(
  driver,
  { artifactDir, evidenceDir, label, width, height, theme, work, task, adopted },
) {
  const themeExpr = `(document.documentElement.dataset.theme ?? 'dark') === ${JSON.stringify(theme)}`;
  const ready = `${themeExpr} && (() => {
    const button = document.querySelector('[data-testid="spawn-submit"]');
    const slot = document.querySelector('[data-testid="spawn-model-slot"]');
    const value = document.querySelector('[data-testid="spawn-model-value"]');
    const editor = document.querySelector('[data-testid="spawn-prompt-card"] [contenteditable="true"]');
    const tiles = document.querySelectorAll('[data-testid="spawn-attachments"] [data-testid="attachment-tile"]');
    if (!button || button.disabled || !slot || !editor) return false;
    if (slot.dataset.state !== 'chosen') return false;
    if (!(value?.textContent ?? '').includes(${JSON.stringify(adopted)})) return false;
    if (tiles.length !== 1) return false;
    if (document.querySelector('[role="dialog"]') || document.querySelector('[role="option"]')) return false;
    return document.activeElement === editor;
  })()`;
  try {
    await driver.waitPage(ready, { label: `${label} ${width}x${height} ${theme} acquisition` });
  } catch (error) {
    const state = await evaluate(driver.send, `(() => ({
      theme: document.documentElement.dataset.theme ?? 'dark',
      viewport: { width: innerWidth, height: innerHeight },
      dialog: document.querySelector('[role="dialog"]') !== null,
      option: document.querySelector('[role="option"]')?.id ?? null,
      disabled: document.querySelector('[data-testid="spawn-submit"]')?.disabled ?? null,
      slot: document.querySelector('[data-testid="spawn-model-slot"]')?.dataset.state ?? null,
      model: document.querySelector('[data-testid="spawn-model-value"]')?.textContent?.trim() ?? '',
      tiles: document.querySelectorAll('[data-testid="spawn-attachments"] [data-testid="attachment-tile"]').length,
      focus: document.activeElement?.tagName + '#' + (document.activeElement?.id ?? ''),
      body: document.body.innerText.slice(0, 400)
    }))()`);
    throw new Error(`${error.message}; acquisition state ${JSON.stringify(state)}`);
  }
  const detail = await evaluate(driver.send, `(() => {
    const editor = document.querySelector('[data-testid="spawn-prompt-card"] [contenteditable="true"]');
    return { theme: document.documentElement.dataset.theme ?? 'dark',
      viewport: { width: innerWidth, height: innerHeight },
      modal: document.querySelector('[role="dialog"]') !== null,
      option: document.querySelector('[role="option"]')?.id ?? null,
      modelState: document.querySelector('[data-testid="spawn-model-slot"]')?.dataset.state,
      model: document.querySelector('[data-testid="spawn-model-value"]')?.textContent?.trim() ?? '',
      startEnabled: !document.querySelector('[data-testid="spawn-submit"]')?.disabled,
      task: editor?.textContent ?? '',
      tiles: document.querySelectorAll('[data-testid="spawn-attachments"] [data-testid="attachment-tile"]').length,
      cwd: document.querySelector('#spawn-cwd')?.getAttribute('aria-label') ?? '',
      focused: document.activeElement === editor,
      focus: document.activeElement?.tagName + '#' + (document.activeElement?.id ?? ''),
      pageWidth: document.documentElement.scrollWidth };
  })()`);
  if (!detail.focused) throw new Error(`${label} ${width} ${theme}: composer lost focus: ${detail.focus}`);
  if (!detail.task.includes(task)) throw new Error(`${label} ${width} ${theme}: task draft lost`);
  if (detail.tiles !== 1) throw new Error(`${label} ${width} ${theme}: attachment draft lost`);
  if (!detail.cwd.includes(work)) throw new Error(`${label} ${width} ${theme}: directory draft lost`);
  const filename = `acquisition-${label}-${width}-${theme}.png`;
  const image = await driver.send("Page.captureScreenshot", { format: "png" });
  writeFileSync(path.join(artifactDir, filename), Buffer.from(image.result.data, "base64"));
  if (evidenceDir) copyFileSync(path.join(artifactDir, filename), path.join(evidenceDir, filename));
  const jsonName = `acquisition-${label}-${width}-${theme}.json`;
  writeFileSync(path.join(artifactDir, jsonName), `${JSON.stringify({ filename, ...detail }, null, 2)}\n`);
  if (evidenceDir) copyFileSync(path.join(artifactDir, jsonName), path.join(evidenceDir, jsonName));
  return detail;
}

// The launched reply at the journey's real viewport/theme, behind no dialog.
export async function captureSession(driver, { artifactDir, evidenceDir, label, width, height, theme }) {
  const themeExpr = `(document.documentElement.dataset.theme ?? 'dark') === ${JSON.stringify(theme)}`;
  await driver.waitPage(
    `${themeExpr} && !!document.querySelector('[data-testid="spawn-submit"]') === false && document.body.innerText.includes('ONBOARDING_REPLY_72e') && !document.querySelector('[role="dialog"]')`,
    { label: `${label} ${width}x${height} ${theme} reply` },
  );
  const filename = `session-${label}-${width}-${theme}.png`;
  const image = await driver.send("Page.captureScreenshot", { format: "png" });
  writeFileSync(path.join(artifactDir, filename), Buffer.from(image.result.data, "base64"));
  if (evidenceDir) copyFileSync(path.join(artifactDir, filename), path.join(evidenceDir, filename));
  const jsonName = `session-${label}-${width}-${theme}.json`;
  writeFileSync(path.join(artifactDir, jsonName), `${JSON.stringify({ filename, phase: "session", theme, viewport: { width, height } }, null, 2)}\n`);
  if (evidenceDir) copyFileSync(path.join(artifactDir, jsonName), path.join(evidenceDir, jsonName));
}

// Optional recording-only access-view capture. It navigates and resizes, so it
// is presentation evidence, never the acquisition/focus journey.
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
