// Optional real-time CDP capture. Frames stay in the Go test's disposable root.
// The annotation is rendered offscreen, never inserted into production UI.

import { spawnSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";

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
