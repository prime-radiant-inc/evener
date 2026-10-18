// Shared by the production onboarding walkthrough and its real-browser regression.
export async function button(driver, pause, name) {
  // Recording pace belongs before readiness, never between observation and use.
  await pause();
  const label = JSON.stringify(name);
  await driver.waitPage(
    `(() => {
      const b = [...document.querySelectorAll('button')].find(b =>
        b.textContent.trim() === ${label} || (b.getAttribute('aria-label') ?? '').trim() === ${label});
      if (!b || b.matches(':disabled, [aria-disabled="true"]')) return false;
      b.scrollIntoView({ block: 'center' });
      const r = b.getBoundingClientRect();
      if (!r.width || !r.height || getComputedStyle(b).visibility !== 'visible') return false;
      if (!b.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2))) return false;
      b.click();
      return true;
    })()`,
    { label: `button ${name}` },
  );
}
