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
 * Dark is the product default, not a mirror of the OS setting: the surface
 * system is designed on true black and that is the intended first impression.
 * "System" therefore has to be a stored value of its own rather than the
 * absence of one — with dark as the fallback, an empty store can no longer mean
 * "follow the OS". The three states are:
 *
 *   "dark"   (or nothing stored) → .theme-dark
 *   "light"                      → .theme-light
 *   "system"                     → no class; app.css's prefers-color-scheme
 *                                  media query decides
 *
 * app.js's theme module writes these and must agree on all three; it stores
 * "system" explicitly for exactly this reason.
 */
(() => {
  let stored = null;
  try {
    stored = localStorage.getItem("tenancy.theme");
  } catch (_) {
    /* storage blocked (private mode, embedded webview): fall through to dark */
  }
  if (stored === "system") return;
  document.documentElement.classList.add(stored === "light" ? "theme-light" : "theme-dark");
})();
