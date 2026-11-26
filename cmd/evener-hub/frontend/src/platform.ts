const appleMobile = /iPhone|iPad|iPod/;

export function isApplePlatform(): boolean {
  return (
    /Mac/.test(navigator.platform) || appleMobile.test(navigator.platform) || appleMobile.test(navigator.userAgent)
  );
}

export function isAppleTouchDevice(): boolean {
  // iPadOS desktop browsing identifies as a Mac, including in a Home Screen app.
  return appleMobile.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
}
