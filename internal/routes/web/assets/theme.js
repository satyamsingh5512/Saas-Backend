/* Theme bootstrap.
 *
 * Loaded synchronously in <head> — deliberately NOT deferred — so the theme
 * class lands on <html> before first paint. A deferred or DOMContentLoaded
 * version produces a visible flash of the wrong theme for anyone who has
 * overridden their OS preference.
 *
 * It cannot be an inline <script> because the server sends
 * `script-src 'self'`, which blocks inline script. A separate same-origin file
 * is the CSP-compatible equivalent.
 *
 * With no stored override, no class is set and the stylesheet's
 * prefers-color-scheme media query decides. That keeps "system" the true
 * default rather than a JS-computed guess.
 */
(() => {
  try {
    const stored = localStorage.getItem("tenancy.theme");
    if (stored === "dark" || stored === "light") {
      document.documentElement.classList.add(`theme-${stored}`);
    }
  } catch (_) {
    /* storage blocked (private mode, embedded webview): fall back to system */
  }
})();
