// browser.mjs — the one place that knows how to get an emulated iPhone
// browser: load the globally installed Playwright, launch its Chromium (with
// a fallback when the cache holds a different revision than Playwright
// pins), and open a context that looks like an iPhone. Shared by driver.mjs
// (participants) and ../smoke.mjs (the prototype's regression check).

import { createRequire } from 'node:module';
import { execSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

export const PHONE_WIDTH = 393;
export const PHONE_HEIGHT = 852;

// One shared iOS Safari UA string: public, unchanging platform boilerplate.
export const IOS_USER_AGENT =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1';

// Playwright is not a project dependency (this directory has none by
// design); it is installed globally, so load it from the global root.
export function loadPlaywright() {
  const globalRoot = execSync('npm root -g').toString().trim();
  return createRequire(import.meta.url)(path.join(globalRoot, 'playwright'));
}

// The globally installed Playwright pins an exact Chromium revision. If the
// cache holds a different revision (another, newer Playwright fetched
// browsers there first), launch() fails with "Executable doesn't exist"
// even though a usable Chromium is sitting right there. Fall back to it
// rather than downloading anything.
function findCachedChromiumExecutable() {
  const cacheDir = path.join(os.homedir(), 'Library', 'Caches', 'ms-playwright');
  if (!fs.existsSync(cacheDir)) return null;
  const revisions = fs
    .readdirSync(cacheDir)
    .filter((name) => /^chromium-\d+$/.test(name))
    .sort()
    .reverse();
  for (const revision of revisions) {
    for (const macDir of ['chrome-mac-arm64', 'chrome-mac']) {
      const appPath = path.join(
        cacheDir, revision, macDir, 'Google Chrome for Testing.app', 'Contents', 'MacOS', 'Google Chrome for Testing',
      );
      if (fs.existsSync(appPath)) return appPath;
    }
  }
  return null;
}

export async function launchChromium(chromium, log = (m) => console.error(m)) {
  try {
    return await chromium.launch({ headless: true });
  } catch (err) {
    if (!/Executable doesn't exist/.test(err.message)) throw err;
    const fallback = findCachedChromiumExecutable();
    if (!fallback) throw err;
    log(`playwright's pinned chromium is missing; falling back to ${fallback}`);
    return chromium.launch({ headless: true, executablePath: fallback });
  }
}

export function newPhoneContext(browser, { scheme = 'light' } = {}) {
  return browser.newContext({
    viewport: { width: PHONE_WIDTH, height: PHONE_HEIGHT },
    deviceScaleFactor: 3,
    isMobile: true,
    hasTouch: true,
    userAgent: IOS_USER_AGENT,
    colorScheme: scheme,
    locale: 'en-US',
    timezoneId: 'America/Los_Angeles',
  });
}
