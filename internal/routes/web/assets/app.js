/* ============================================================================
   Tenancy — workspace dashboard
   ----------------------------------------------------------------------------
   Dependency-free SPA served from the Go binary.

   Two invariants hold everywhere in this file:

   1. DOM is never built from strings. Every node comes from el(), and all text
      is assigned through textContent. In a multi-tenant product, names, emails,
      and audit metadata are attacker-influenced, so interpolating them into
      innerHTML would be a stored-XSS vector. el() throws on `html` to enforce it.

   2. The UI hides what the caller cannot do, but never relies on that. Every
      action is authorized server-side by permission or API key scope; hiding a
      control only avoids dead ends.
   ========================================================================== */

(() => {
  "use strict";

  const API = "/api/v1";
  const THEME_KEY = "tenancy.theme";
  const SESSION_KEY = "tenancy.session";

  /* ──────────────────────────────── State ──────────────────────────────── */

  const state = {
    access: null,
    refresh: null,
    profile: null,
    perms: new Set(),
    inviteToken: null,
    route: "overview",
  };

  let signedOut = false;
  // Changes only when the authenticated identity is replaced or cleared. Access
  // token rotation stays within an epoch, so same-session 401s may singleflight.
  let sessionEpoch = 0;

  // sessionStorage, not localStorage: the token dies with the tab. A durable
  // cross-device session needs HttpOnly cookies issued by the API, which this
  // client deliberately does not imitate with JS-readable storage.
  const session = {
    save() {
      try {
        sessionStorage.setItem(SESSION_KEY, JSON.stringify({ a: state.access, r: state.refresh }));
      } catch (_) { /* storage unavailable: session stays in memory */ }
    },
    load() {
      sessionEpoch += 1;
      try {
        const raw = sessionStorage.getItem(SESSION_KEY);
        if (!raw) return false;
        const p = JSON.parse(raw);
        state.access = p.a || null;
        state.refresh = p.r || null;
        return Boolean(state.access);
      } catch (_) { return false; }
    },
    clear() {
      try { sessionStorage.removeItem(SESSION_KEY); } catch (_) { /* ignore */ }
      state.access = null;
      state.refresh = null;
      state.profile = null;
      state.perms = new Set();
    },
  };

  /* ───────────────────────────────── DOM ───────────────────────────────── */

  const $ = (s) => document.querySelector(s);
  const $$ = (s) => Array.from(document.querySelectorAll(s));

  function el(tag, props, ...kids) {
    const node = document.createElement(tag);
    if (props) {
      for (const [k, v] of Object.entries(props)) {
        if (v === null || v === undefined || v === false) continue;
        if (k === "class") node.className = v;
        else if (k === "text") node.textContent = v;
        else if (k === "html") throw new Error("el(): raw html is not permitted");
        // A style attribute is silently dropped by style-src 'self', so it would
        // appear to work locally and fail invisibly under the real CSP.
        else if (k === "style") throw new Error("el(): use a class, not an inline style");
        else if (k === "dataset") { for (const [d, dv] of Object.entries(v)) node.dataset[d] = dv; }
        else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2).toLowerCase(), v);
        else if (v === true) node.setAttribute(k, "");
        else node.setAttribute(k, String(v));
      }
    }
    for (const kid of kids.flat()) {
      if (kid === null || kid === undefined || kid === false) continue;
      node.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
    }
    return node;
  }

  function icon(name, cls = "icon icon--sm") {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", cls);
    svg.setAttribute("aria-hidden", "true");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", `#i-${name}`);
    svg.append(use);
    return svg;
  }

  const clear = (n) => { while (n.firstChild) n.removeChild(n.firstChild); return n; };

  /* ─────────────────────────────── Motion ─────────────────────────────── */

  /* Entrances are entirely in app.css and need nothing here: a fresh element
     carrying a keyframe animation plays it the first time it is rendered, which
     is the moment it appears. Since every render in this file builds new nodes
     rather than mutating old ones, "first render" and "appears" are the same
     event, and the stylesheet gets that for free.

     Exits cannot work that way, for three different reasons that happen to need
     the same fix: a <dialog> leaves the top layer in the same frame close() is
     called, a toast is removed from the DOM, and the drawer scrim is switched
     off with [hidden]. In each case the element the animation would run on is
     already gone. So the class goes on here, and the removal is deferred until
     the animation has had time to play.

     The wait is a timeout and not an `animationend` listener, deliberately. A
     listener is more precise and has one failure mode too many: if the event
     never arrives — the animation is cancelled, the tab is backgrounded, the
     rule is edited away — the dialog stays open forever, which is a functional
     break, not a cosmetic one. A timeout always fires. The cost of that choice
     is that these numbers duplicate durations in app.css and have to be changed
     with them; the token each one mirrors is named below. */
  const EXIT = {
    overlay: 150, // --t      · .modal, .sheet, .palette and their ::backdrop
    toast: 100,   // --t-fast · .toast
    scrim: 250,   // --t-slow · .scrim — matched to the drawer so both leave together
  };

  /* Read here as well as in the stylesheet, and not redundantly: the stylesheet
     can shorten an animation to nothing, but only this can skip the *wait in
     front of the close*. Without it a reduced-motion reader would sit through
     150ms of nothing before every dialog dismissal — the delay would survive
     exactly the preference meant to remove it. */
  const reducedMotion = () => window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  const exitTimers = new WeakMap();

  /** Plays `node`'s exit animation, then runs `done`. Idempotent per node. */
  function playExit(node, ms, done) {
    if (!node) return;
    if (reducedMotion()) { done(); return; }
    clearTimeout(exitTimers.get(node));
    node.classList.add("is-closing");
    exitTimers.set(node, setTimeout(() => {
      exitTimers.delete(node);
      node.classList.remove("is-closing");
      done();
    }, ms));
  }

  /** Aborts a pending exit, so reopening a still-departing overlay is clean. */
  function cancelExit(node) {
    if (!node) return;
    clearTimeout(exitTimers.get(node));
    exitTimers.delete(node);
    node.classList.remove("is-closing");
  }

  const dismiss = (dlg) => { if (dlg && dlg.open) playExit(dlg, EXIT.overlay, () => dlg.close()); };

  /* ─────────────────────────────── Format ─────────────────────────────── */

  const fmt = {
    date(v) {
      if (!v) return "—";
      const d = new Date(v);
      return isNaN(d) ? "—" : d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
    },
    dateTime(v) {
      if (!v) return "—";
      const d = new Date(v);
      return isNaN(d) ? "—" : d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
    },
    ago(v) {
      if (!v) return "";
      const mins = Math.round((Date.now() - new Date(v).getTime()) / 60000);
      if (mins < 1) return "just now";
      if (mins < 60) return `${mins}m ago`;
      const h = Math.round(mins / 60);
      if (h < 24) return `${h}h ago`;
      const d = Math.round(h / 24);
      return d < 30 ? `${d}d ago` : fmt.date(v);
    },
    // A zero price only means "free" for the free plan. The seeded enterprise
    // plan is also 0 because it is quote-based.
    price(plan) {
      if (plan.price_cents) return `$${(plan.price_cents / 100).toFixed(0)}`;
      return plan.code === "free" ? "Free" : "Custom";
    },
    limit(v) { return v === null || v === undefined ? "Unlimited" : String(v); },
    initials(name, fallback) {
      const src = (name || fallback || "?").trim();
      const parts = src.split(/\s+/).filter(Boolean);
      if (!parts.length) return "?";
      if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
      return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
    },
    words(v) { return String(v || "").replace(/[._]/g, " "); },
    title(v) {
      const s = fmt.words(v);
      return s ? s[0].toUpperCase() + s.slice(1) : s;
    },
  };

  /* ─────────────────────────────── Toasts ─────────────────────────────── */

  function toast(msg, kind = "info") {
    const glyph = kind === "ok" ? "check-circle" : kind === "err" ? "alert" : "info";
    const node = el("div", { class: `toast toast--${kind}`, role: kind === "err" ? "alert" : "status" },
      icon(glyph, "icon icon--sm toast__icon"),
      el("p", { class: "toast__text", text: msg }));
    $("#toasts").append(node);
    // Errors linger: they carry something the reader may still need to act on.
    // Either way the toast now animates out before it leaves the DOM, so the
    // dismissal is visible as a departure rather than as a node disappearing.
    setTimeout(() => playExit(node, EXIT.toast, () => node.remove()), kind === "err" ? 7000 : 4500);
  }

  // BUTTON_BUSY_BEGIN
  function pendingButtonLabel(label) {
    if (label.startsWith("Sign in")) return "Signing in…";
    const first = label.split(/\s+/)[0];
    const verbs = {
      Save: "Saving…", Create: "Creating…", Send: "Sending…", Add: "Adding…",
      Change: "Changing…", Accept: "Accepting…", Retry: "Retrying…", Reload: "Loading…",
      Delete: "Deleting…", Revoke: "Revoking…", Cancel: "Canceling…", Switch: "Switching…",
      Remove: "Removing…",
    };
    return verbs[first] || "Working…";
  }

  /** Replaces a button's children while pending and restores the exact nodes. */
  function setButtonBusy(button, label) {
    const children = Array.from(button.childNodes);
    const wasDisabled = button.disabled;
    const previousBusy = button.getAttribute("aria-busy");
    let restored = false;

    button.disabled = true;
    button.setAttribute("aria-busy", "true");
    clear(button).append(icon("refresh", "icon spinner"), label);

    return () => {
      if (restored) return;
      restored = true;
      clear(button).append(...children);
      button.disabled = wasDisabled;
      if (previousBusy === null) button.removeAttribute("aria-busy");
      else button.setAttribute("aria-busy", previousBusy);
    };
  }
  // BUTTON_BUSY_END

  /* ─────────────────────────────── API ────────────────────────────────── */

  class ApiError extends Error {
    constructor(status, code, message) {
      super(message || "Request failed");
      this.status = status;
      this.code = code;
    }
  }

  const isAbort = (err) => Boolean(err && err.name === "AbortError");
  const isAuthoritativeAuthError = (err) => Boolean(err && err.status === 401);

  // ASYNC_ERROR_VISIBILITY_BEGIN
  function shouldSurfaceAsyncError(err, ownership = null) {
    return !isAbort(err) && (!ownership || ownership.owns());
  }
  // ASYNC_ERROR_VISIBILITY_END

  // REFRESH_SINGLEFLIGHT_BEGIN
  function createSingleflight(task) {
    let inFlight = null;
    return function run() {
      if (!inFlight) {
        inFlight = Promise.resolve().then(task).finally(() => { inFlight = null; });
      }
      return inFlight;
    };
  }
  // REFRESH_SINGLEFLIGHT_END

  // AUTH_REQUEST_BEGIN
  function staleSessionError() {
    const err = new Error("The session changed while this request was in flight.");
    err.name = "AbortError";
    return err;
  }

  async function call(path, opts = {}, retried = false, requestEpoch = sessionEpoch) {
    const { method = "GET", body, auth = true, raw = false, signal, headers: extraHeaders = {} } = opts;

    const headers = { Accept: "application/json", ...extraHeaders };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const accessUsed = auth ? state.access : null;
    if (accessUsed) headers.Authorization = `Bearer ${accessUsed}`;

    let res;
    try {
      res = await fetch(API + path, { method, headers, signal, body: body === undefined ? undefined : JSON.stringify(body) });
    } catch (err) {
      if (isAbort(err)) throw err;
      throw new ApiError(0, "NETWORK", "Cannot reach the server.");
    }

    // Never replay or interpret a response under a replacement login. This is
    // especially important for POST bodies: an old-session 401 must not retry
    // the mutation with a newly signed-in user's Bearer token.
    if (auth && requestEpoch !== sessionEpoch) throw staleSessionError();

    // Each caller retries at most once. If another caller already rotated while
    // this request was in flight, retry with that token without rotating again.
    if (res.status === 401 && auth && !retried && state.refresh) {
      if (accessUsed && accessUsed !== state.access) return call(path, opts, true, requestEpoch);
      const refreshUsed = state.refresh;
      try {
        await rotate();
      } catch (err) {
        // A refresh result belongs only to the epoch and credential that began
        // it. A delayed old refresh must never clear a replacement login.
        if (requestEpoch !== sessionEpoch || state.refresh !== refreshUsed) throw staleSessionError();
        // A network/5xx refresh failure is not proof that the credential died.
        // Keep it for a later retry; only this session's refresh-endpoint 401 is final.
        if (isAuthoritativeAuthError(err)) {
          signOut(true);
          throw new ApiError(401, "UNAUTHORIZED", "Your session expired. Sign in again.");
        }
        throw err;
      }
      if (requestEpoch !== sessionEpoch) throw staleSessionError();
      return call(path, opts, true, requestEpoch);
    }
    if (res.status === 401 && auth && (retried || !state.refresh)) {
      if (requestEpoch !== sessionEpoch) throw staleSessionError();
      signOut(true);
    }

    const payload = res.status === 204 ? null : await res.json().catch(() => null);

    if (!res.ok) {
      const e = (payload && payload.error) || {};
      throw new ApiError(res.status, e.code || "ERROR", e.message || `Request failed (${res.status})`);
    }
    if (raw) {
      return { items: (payload && payload.data) || [], page: (payload && payload.meta && payload.meta.pagination) || null };
    }
    return payload ? payload.data : null;
  }

  const list = (path, opts = {}) => call(path, { ...opts, raw: true });

  const rotate = createSingleflight(async () => {
    const rotationEpoch = sessionEpoch;
    const refreshUsed = state.refresh;
    const d = await call("/auth/refresh", { method: "POST", body: { refresh_token: refreshUsed }, auth: false }, true, rotationEpoch);
    if (rotationEpoch !== sessionEpoch || state.refresh !== refreshUsed) throw staleSessionError();
    state.access = d.access_token;
    state.refresh = d.refresh_token;
    session.save();
    return d;
  });
  // AUTH_REQUEST_END

  const can = (code) => state.perms.has(code);

  /* ─────────────────────────────── Theme ──────────────────────────────── */

  const theme = {
    // Dark is the default, so an empty store means dark rather than "system".
    // "system" is stored explicitly; see the note in theme.js, which has to
    // resolve the same three states before first paint.
    current() {
      try { return localStorage.getItem(THEME_KEY) || "dark"; } catch (_) { return "dark"; }
    },
    // The raw stored value, or null when this device has never chosen. current()
    // cannot answer that any more: it resolves an empty store to "dark", which
    // is now indistinguishable from an explicit dark choice. loadProfile() needs
    // the distinction to know whether the server preference may take over.
    stored() {
      try { return localStorage.getItem(THEME_KEY); } catch (_) { return null; }
    },
    apply(mode) {
      const root = document.documentElement;
      root.classList.remove("theme-dark", "theme-light");
      if (mode === "dark" || mode === "light") root.classList.add(`theme-${mode}`);
      try {
        localStorage.setItem(THEME_KEY, mode);
      } catch (_) { /* storage blocked */ }
    },
    // Cycles dark → light → system so all three are reachable from one control,
    // starting from the default rather than passing through it.
    cycle() {
      const order = ["dark", "light", "system"];
      const next = order[(order.indexOf(theme.current()) + 1) % order.length];
      theme.apply(next);
      toast(`Theme: ${next}`, "info");
      return next;
    },
  };

  /* ────────────────────────────── Overlays ─────────────────────────────── */

  const modal = $("#modal");

  function closeModal() { dismiss(modal); }

  function openModal({ title, desc, body, footer, wide = false, ownership = null }) {
    const activeOwnership = ownership || actionOwner.begin();
    if (!activeOwnership.owns()) return false;
    // A modal can be reopened while a previous dismissal is still animating —
    // most often by the palette, which runs a create action as it leaves. Without
    // this the pending timeout would close the modal that just opened.
    cancelExit(modal);
    modal.classList.toggle("modal--wide", wide);
    const head = el("div", { class: "modal__head" },
      el("div", {},
        el("h2", { class: "modal__title", id: "modal-title", text: title }),
        desc ? el("p", { class: "modal__desc", text: desc }) : null),
      el("button", { class: "icon-btn", type: "button", "aria-label": "Close", onclick: closeModal }, icon("x")));
    clear(modal).append(...[head, body, footer].filter((region) => region !== null && region !== undefined && region !== false));
    if (!modal.open) modal.showModal();
    /* No `.btn--danger` here, deliberately. querySelector resolves in document
       order, and confirmModal passes no body, so a danger button was always the
       first match — "Delete project?" opened with Delete already focused, one
       stray Enter from destroying something. With it removed, confirmModal has
       no match at all and the dialog's own focusing steps take over, which land
       on the first focusable descendant: the header close button. */
    const focusable = modal.querySelector("input, select, textarea, .btn--primary");
    if (focusable) focusable.focus();
    return true;
  }

  /* Mobile drawer. Module scope rather than local to wireShell() because route()
     also has to close it. In drawer mode it behaves like a modal surface while
     retaining the existing aside, transition, and scrim. */
  // DRAWER_LIFECYCLE_BEGIN
  const drawerMedia = window.matchMedia("(max-width: 1024px)");
  let navOpener = null;

  const drawerFocusable = (sidebar) => Array.from(sidebar.querySelectorAll(
    'a[href]:not([hidden]), button:not([disabled]):not([hidden]), input:not([disabled]):not([hidden]), select:not([disabled]):not([hidden]), textarea:not([disabled]):not([hidden]), [tabindex]:not([tabindex="-1"]):not([hidden])',
  ));

  function setDrawerBackgroundInert(inert) {
    [$(".topbar"), host()].filter(Boolean).forEach((node) => {
      node.inert = inert;
      if (inert) node.setAttribute("inert", "");
      else node.removeAttribute("inert");
    });
  }

  function openNav(event) {
    if (!drawerMedia.matches) return;
    const sidebar = $("#sidebar");
    const scrim = $(".scrim");
    navOpener = (event && event.currentTarget) || document.activeElement || $('.topbar__menu[data-nav="open"]');
    cancelExit(scrim);
    document.body.classList.add("nav-open");
    $$('[data-nav="open"]').forEach((b) => b.setAttribute("aria-expanded", "true"));
    sidebar.setAttribute("role", "dialog");
    sidebar.setAttribute("aria-modal", "true");
    sidebar.setAttribute("aria-label", "Navigation");
    setDrawerBackgroundInert(true);
    // Order matters: the fade-in is a keyframe animation, which only plays as the
    // element goes from [hidden] to rendered, so unhiding precedes focus.
    scrim.hidden = false;
    const current = sidebar.querySelector('.nav__item[aria-current="page"]:not([hidden])');
    const first = drawerFocusable(sidebar)[0];
    (current || first || sidebar).focus();
  }

  function closeNav({ restoreFocus = true } = {}) {
    const sidebar = $("#sidebar");
    const scrim = $(".scrim");
    const wasOpen = document.body.classList.contains("nav-open");
    document.body.classList.remove("nav-open");
    $$('[data-nav="open"]').forEach((b) => b.setAttribute("aria-expanded", "false"));
    sidebar.removeAttribute("role");
    sidebar.removeAttribute("aria-modal");
    sidebar.removeAttribute("aria-label");
    setDrawerBackgroundInert(false);

    const opener = navOpener;
    navOpener = null;
    if (wasOpen && restoreFocus && opener && typeof opener.focus === "function") opener.focus();
    // route() calls this on every navigation, including the desktop case where
    // the drawer was never open. Avoid delaying an already-hidden scrim.
    if (scrim.hidden) { cancelExit(scrim); return; }
    playExit(scrim, EXIT.scrim, () => { scrim.hidden = true; });
  }

  function trapNavFocus(event) {
    if (event.key !== "Tab" || !drawerMedia.matches || !document.body.classList.contains("nav-open") ||
        document.querySelector("dialog[open]")) return false;
    const sidebar = $("#sidebar");
    const focusable = drawerFocusable(sidebar);
    if (!focusable.length) { event.preventDefault(); sidebar.focus(); return true; }
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (!sidebar.contains(document.activeElement)) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    } else if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
    return true;
  }
  // DRAWER_LIFECYCLE_END

  /**
   * formModal renders a titled form. Fields are descriptors; onSubmit receives
   * the collected values and may throw an ApiError to surface an inline error.
   */
  function formModal({ title, desc, submit, fields, onSubmit, danger = false, wide = false, submitWhenChanged = false, ownership = null }) {
    const activeOwnership = ownership || actionOwner.begin();
    if (!activeOwnership.owns()) return false;
    const alert = el("div", { class: "alert", id: "modal-form-error", role: "alert", hidden: true });
    const reads = [];
    const body = el("div", { class: "modal__body" }, alert);

    for (const f of fields) {
      const id = `f-${f.name}`;
      const labelId = `${id}-label`;
      // A picker is a set of checkboxes, not a labelable control, so `for` had
      // nothing to point at. It gets a group role and is named by reference.
      const isPicker = f.type === "picker";
      let control;

      if (f.type === "select") {
        control = el("select", { class: "select", id });
        for (const o of f.options) {
          control.append(el("option", { value: o.value, selected: String(o.value) === String(f.value) }, o.label));
        }
      } else if (f.type === "textarea") {
        control = el("textarea", { class: "textarea", id, rows: 3, placeholder: f.placeholder || "" });
        control.value = f.value || "";
      } else if (f.type === "checkbox") {
        control = el("input", { type: "checkbox", id });
        control.checked = Boolean(f.value);
      } else if (isPicker) {
        control = el("div", { class: "picker", id, role: "group", "aria-labelledby": labelId });
        for (const o of f.options) {
          const box = el("input", { type: "checkbox", value: o.code, id: `p-${o.code}` });
          box.checked = (f.value || []).includes(o.code);
          control.append(el("label", { class: "picker__opt", for: `p-${o.code}` }, box,
            el("span", {},
              el("code", { class: "picker__code", text: o.code }),
              o.description ? el("small", { class: "picker__desc", text: o.description }) : null)));
        }
      } else {
        control = el("input", {
          class: "input", id, type: f.type || "text",
          placeholder: f.placeholder || "", required: f.required || false, minlength: f.minlength,
        });
        if (f.value !== undefined && f.value !== null) control.value = f.value;
      }

      if (!isPicker) {
        if (f.required) control.required = true;
        if (f.minlength !== undefined) control.minLength = f.minlength;
        if (f.maxlength !== undefined) control.maxLength = f.maxlength;
        if (f.hint) control.setAttribute("aria-describedby", `${id}-hint`);
      }

      if (f.type === "checkbox") {
        body.append(el("div", { class: "check" }, control,
          el("div", {}, el("label", { class: "check__label", for: id, text: f.label }),
            f.hint ? el("p", { class: "check__hint", id: `${id}-hint`, text: f.hint }) : null)));
      } else {
        body.append(el("div", { class: "field" },
          el("div", { class: "field__row" },
            isPicker
              ? el("span", { class: "field__label", id: labelId, text: f.label })
              : el("label", { class: "field__label", for: id, text: f.label }),
            f.optional ? el("span", { class: "field__optional", text: "optional" }) : null),
          control,
          f.hint ? el("p", { class: "field__hint", id: `${id}-hint`, text: f.hint }) : null));
      }

      reads.push({
        name: f.name,
        read() {
          if (f.type === "checkbox") return control.checked;
          if (f.type === "picker") return Array.from(control.querySelectorAll("input:checked")).map((c) => c.value);
          return control.value.trim();
        },
      });
    }

    const readValues = () => {
      const values = {};
      for (const r of reads) values[r.name] = r.read();
      return values;
    };
    const initialValues = JSON.stringify(readValues());
    const go = el("button", { class: `btn ${danger ? "btn--danger" : "btn--primary"}`, type: "submit" }, submit);
    const syncSubmit = () => {
      go.disabled = submitWhenChanged && JSON.stringify(readValues()) === initialValues;
    };
    if (submitWhenChanged) {
      body.addEventListener("input", syncSubmit);
      body.addEventListener("change", syncSubmit);
      syncSubmit();
    }
    const footer = el("div", { class: "modal__foot" },
      el("button", { class: "btn btn--secondary", type: "button", onclick: closeModal }, "Cancel"),
      go);
    const form = el("form", { class: "modal__form" }, body, footer);
    const clearServerError = () => {
      alert.hidden = true;
      for (const control of form.querySelectorAll("input, select, textarea")) {
        control.removeAttribute("aria-invalid");
        const ids = (control.getAttribute("aria-describedby") || "").split(/\s+/).filter((id) => id && id !== alert.id);
        if (ids.length) control.setAttribute("aria-describedby", ids.join(" "));
        else control.removeAttribute("aria-describedby");
      }
    };
    form.addEventListener("input", clearServerError);
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      clearServerError();
      if (!form.checkValidity()) {
        form.reportValidity();
        return;
      }
      if (!activeOwnership.owns()) { closeModal(); return; }
      const values = readValues();
      const stopBusy = setButtonBusy(go, pendingButtonLabel(submit));
      try {
        await onSubmit(values, activeOwnership);
        if (activeOwnership.owns()) closeModal();
      } catch (err) {
        if (!shouldSurfaceAsyncError(err, activeOwnership)) return;
        alert.textContent = err.message;
        if (err.actionLabel && typeof err.onAction === "function") {
          const action = el("button", { class: "btn btn--secondary btn--sm", type: "button" }, err.actionLabel);
          action.addEventListener("click", async () => {
            const stopBusy = setButtonBusy(action, pendingButtonLabel(err.actionLabel));
            try {
              await err.onAction();
            } catch (actionErr) {
              if (!shouldSurfaceAsyncError(actionErr, activeOwnership)) return;
              alert.textContent = actionErr.message;
              alert.hidden = false;
            } finally {
              stopBusy();
            }
          });
          alert.append(el("p", { text: "Reload to review the latest grants before saving again." }), action);
        }
        const invalidValues = err.status === 400 || err.code === "VALIDATION";
        for (const control of form.querySelectorAll("input, select, textarea")) {
          if (invalidValues) control.setAttribute("aria-invalid", "true");
          const described = control.getAttribute("aria-describedby");
          control.setAttribute("aria-describedby", [described, alert.id].filter(Boolean).join(" "));
        }
        alert.hidden = false;
      } finally {
        stopBusy();
        syncSubmit();
      }
    });

    return openModal({ title, desc, wide, body: form, ownership: activeOwnership });
  }

  function confirmModal({ title, desc, confirm, onConfirm, ownership = null }) {
    const activeOwnership = ownership || actionOwner.begin();
    if (!activeOwnership.owns()) return false;
    const go = el("button", { class: "btn btn--danger", type: "button" }, confirm);
    go.addEventListener("click", async () => {
      const stopBusy = setButtonBusy(go, pendingButtonLabel(confirm));
      try {
        await onConfirm(activeOwnership);
        if (activeOwnership.owns()) closeModal();
      } catch (err) {
        if (!shouldSurfaceAsyncError(err, activeOwnership)) return;
        toast(err.message, "err");
        closeModal();
      } finally {
        if (activeOwnership.owns()) stopBusy();
      }
    });
    openModal({
      title, desc,
      footer: el("div", { class: "modal__foot" },
        el("button", { class: "btn btn--secondary", type: "button", onclick: closeModal }, "Cancel"),
        go),
      ownership: activeOwnership,
    });
  }

  /* ───────────────────────────── Components ───────────────────────────── */

  function badge(text, tone = "") {
    return el("span", { class: `badge${tone ? ` badge--${tone}` : ""}` },
      tone ? el("span", { class: "badge__dot" }) : null, text);
  }

  const TONES = {
    active: "ok", accepted: "ok", paid: "ok",
    archived: "", expired: "", canceled: "err", revoked: "err", past_due: "err", disabled: "err",
    pending: "warn", trialing: "warn", invited: "warn",
  };
  const statusBadge = (s) => badge(fmt.title(s), TONES[s] === undefined ? "" : TONES[s]);

  function emptyState(glyph, title, text, ...actions) {
    return el("div", { class: "empty" }, icon(glyph, "icon icon--lg"),
      el("p", { class: "empty__title", text: title }),
      el("p", { class: "empty__text", text }),
      actions.filter(Boolean).length ? el("div", { class: "empty__actions" }, actions.filter(Boolean)) : null);
  }

  function inlineState(title, text, onRetry) {
    return el("div", { class: "inline-state", role: "status" },
      icon("alert"),
      el("div", { class: "inline-state__copy" },
        el("p", { class: "inline-state__title", text: title }),
        el("p", { class: "inline-state__text", text })),
      onRetry ? el("button", { class: "btn btn--secondary btn--sm", type: "button", onclick: onRetry }, "Retry") : null);
  }

  function kpi({ label, value, glyph, foot, meter }) {
    const card = el("div", { class: "kpi" },
      el("div", { class: "kpi__top" }, el("span", { class: "kpi__label", text: label }), icon(glyph)),
      el("strong", { class: "kpi__value", text: String(value) }));

    if (meter) {
      const { used, max } = meter;
      const pct = max ? Math.min(100, Math.round((used / max) * 100)) : 0;
      const fill = el("div", { class: `meter__fill${pct >= 100 ? " meter__fill--full" : pct >= 80 ? " meter__fill--warn" : ""}` });
      // A CSSOM write, not a style attribute: CSP restricts parsed inline styles,
      // not programmatic property assignment.
      fill.style.width = max ? `${pct}%` : "100%";
      card.append(el("div", { class: "meter" },
        el("div", { class: "meter__track" }, fill),
        el("div", { class: "meter__meta" },
          el("span", { text: max ? `${used} of ${max}` : `${used} used` }),
          el("span", { text: max ? `${pct}%` : "unlimited" }))));
    } else if (foot) {
      card.append(el("p", { class: "kpi__foot", text: foot }));
    }
    return card;
  }

  /* `label` names the scroll container, not the table. .table-wrap is
     overflow-x: auto, and a scroll container with no focusable child cannot be
     reached by keyboard at all — on a narrow viewport the last columns are
     simply unreachable. tabindex makes it scrollable with the arrow keys, and a
     focus stop that announces only "group" is its own problem, hence the name. */
  function table(cols, rows, label) {
    const head = el("tr", {});
    for (const c of cols) {
      head.append(el("th", { class: c.actions ? "actions" : c.num ? "num" : null, scope: "col" },
        // The actions column is deliberately blank on screen; left empty it is
        // announced as "blank" once per row.
        c.actions ? el("span", { class: "sr-only", text: "Actions" }) : c.label));
    }
    return el("div", { class: "table-wrap", tabindex: 0, role: "group", "aria-label": label || null },
      el("table", { class: "table" }, el("thead", {}, head), el("tbody", {}, rows)));
  }

  // Skeleton rows mirror the real column count so nothing shifts on load.
  function tableSkeleton(cols = 5, rows = 6) {
    const body = [];
    for (let r = 0; r < rows; r++) {
      const tr = el("tr", {});
      for (let c = 0; c < cols; c++) {
        const w = c === 0 ? "skel--w60" : c === cols - 1 ? "skel--w40" : "skel--w80";
        tr.append(el("td", {}, el("span", { class: `skel skel--text ${w}` })));
      }
      body.push(tr);
    }
    const head = el("tr", {});
    for (let c = 0; c < cols; c++) head.append(el("th", { scope: "col" }, el("span", { class: "skel skel--text skel--w40" })));
    return el("div", { class: "table-wrap" },
      el("table", { class: "table" }, el("thead", {}, head), el("tbody", {}, body)));
  }

  function kpiSkeleton(n = 4) {
    const wrap = el("div", { class: "kpis" });
    for (let i = 0; i < n; i++) {
      wrap.append(el("div", { class: "kpi" },
        el("span", { class: "skel skel--text skel--w40" }),
        el("span", { class: "skel skel--kpi skel--w60" })));
    }
    return wrap;
  }

  function pageSkeleton({ title, desc, narrow = false, action = null, content }) {
    const actionFootprint = action
      ? el("span", { class: "btn btn--secondary", "aria-hidden": "true" },
        el("span", { class: "skel skel--icon" }),
        el("span", { class: "skel skel--text skel--action", text: action }))
      : null;
    return el("div", { class: `page${narrow ? " page--narrow" : ""}` },
      pageHead(title, desc, actionFootprint), content);
  }

  function pager(meta, onPage) {
    if (!meta) return null;
    const from = meta.total === 0 ? 0 : (meta.page - 1) * meta.page_size + 1;
    const to = Math.min(meta.page * meta.page_size, meta.total);
    return el("div", { class: "pager" },
      el("span", { text: `${from}–${to} of ${meta.total}` }),
      meta.total_pages > 1 ? el("div", { class: "pager__btns" },
        el("button", { class: "btn btn--secondary btn--sm", type: "button", disabled: meta.page <= 1, dataset: { focusKey: "pager-prev" }, onclick: () => onPage(meta.page - 1) }, "Previous"),
        el("button", { class: "btn btn--secondary btn--sm", type: "button", disabled: meta.page >= meta.total_pages, dataset: { focusKey: "pager-next" }, onclick: () => onPage(meta.page + 1) }, "Next")) : null);
  }

  function pageHead(title, desc, ...actions) {
    return el("header", { class: "page__head" },
      el("div", {}, el("h1", { class: "page__title", text: title }), desc ? el("p", { class: "page__desc", text: desc }) : null),
      actions.filter(Boolean).length ? el("div", { class: "page__actions" }, actions.filter(Boolean)) : null);
  }

  /* `key` defaults to the label, which is enough for every action whose label is
     stable across a re-render. The archive/restore toggle is the one that is
     not — it renames itself as it succeeds — so it passes a fixed key and keeps
     focus on the button the reader just pressed. */
  const rowBtn = (glyph, label, onclick, danger = false, key = label) =>
    el("button", {
      class: `icon-btn${danger ? " icon-btn--danger" : ""}`, type: "button",
      title: label, "aria-label": label, dataset: { focusKey: key }, onclick,
    }, icon(glyph));

  function searchBox(value, placeholder, onChange) {
    const input = el("input", {
      class: "input", type: "search", value, placeholder, "aria-label": placeholder,
      dataset: { focusKey: "search" },
    });
    let t;
    input.addEventListener("input", () => {
      clearTimeout(t);
      t = setTimeout(() => onChange(input.value.trim()), 280);
    });
    return el("div", { class: "search" }, icon("search"), input);
  }

  function segment(options, active, onPick, label) {
    const wrap = el("div", { class: "segment", role: "group", "aria-label": label || null });
    for (const o of options) {
      wrap.append(el("button", {
        class: "segment__btn", type: "button", "aria-pressed": String(o.value === active),
        dataset: { focusKey: `segment-${o.value || "all"}` },
        onclick: () => onPick(o.value),
      }, o.label));
    }
    return wrap;
  }

  async function copy(value) {
    try {
      await navigator.clipboard.writeText(value);
      toast("Copied to clipboard", "ok");
    } catch (_) {
      // Clipboard requires a secure context; absent over plain HTTP.
      toast("Copy unavailable here — select the text manually", "err");
    }
  }

  /* ─────────────────────────────── Pages ──────────────────────────────── */

  const pages = {};
  const host = () => $("#page");
  const render = (node) => clear(host()).append(node);

  // VIEW_OWNERSHIP_BEGIN
  function createGenerationOwner() {
    let generation = 0;
    let controller = null;
    return {
      begin() {
        if (controller) controller.abort();
        controller = new AbortController();
        const mine = ++generation;
        return { generation: mine, signal: controller.signal, owns: () => mine === generation };
      },
      invalidate() {
        generation += 1;
        if (controller) controller.abort();
        controller = null;
      },
    };
  }
  // VIEW_OWNERSHIP_END

  // HONEST_LOAD_BEGIN
  async function honestLoad(allowed, loader) {
    if (!allowed) return { kind: "forbidden" };
    try {
      return { kind: "ready", value: await loader() };
    } catch (error) {
      if (error && error.name === "AbortError") throw error;
      return { kind: "error", error };
    }
  }
  // HONEST_LOAD_END

  // Picker datasets must be complete. Follow pagination up to 1,000 records;
  // beyond that explicit failure is safer than a silently incomplete choice.
  // FETCH_ALL_PAGES_BEGIN
  async function fetchAllPages(path, { signal, cap = 1000, fetchPage = list } = {}) {
    const items = [];
    let page = 1;
    let totalPages = 1;
    do {
      const separator = path.includes("?") ? "&" : "?";
      const result = await fetchPage(`${path}${separator}page=${page}&page_size=100`, { signal });
      const meta = result && result.page;
      if (!meta || !Number.isInteger(meta.page) || !Number.isInteger(meta.total_pages) || typeof meta.total !== "number") {
        throw new Error("The server did not return pagination metadata required to load the complete list.");
      }
      if (meta.total > cap) {
        throw new Error(`This picker contains more than ${cap} records. Narrow the dataset before continuing.`);
      }
      items.push(...result.items);
      if (items.length > cap) {
        throw new Error(`This picker contains more than ${cap} records. Narrow the dataset before continuing.`);
      }
      totalPages = meta.total_pages;
      page += 1;
    } while (page <= totalPages);
    return { items, total: items.length };
  }
  // FETCH_ALL_PAGES_END

  const viewOwner = createGenerationOwner();
  // Async action loaders and their resulting modal share an owner distinct from
  // route rendering. Navigation, sign-out, or a newer action invalidates them.
  const actionOwner = createGenerationOwner();
  const refreshRoute = (name) => { if (state.route === name) pages[name](); };

  /* ── Focus survival ──
     Every list control in this file re-renders the whole page: a filter, a page
     button, and a row action all end in `pages.X()`, which clears #page and
     builds it again. The node holding focus is therefore destroyed, and focus
     falls to <body>. For the search box that is not an inconvenience but a
     functional break — the 280ms debounce fires mid-typing, so the second word
     of a query cannot be typed without tabbing back.

     A control opts in with `dataset: { focusKey }`. The key does not have to be
     unique: row actions legitimately repeat ("Edit" on every row), so the
     ordinal among same-key nodes is recorded too and the same position is
     restored. That is what puts focus back on row 3's Edit button rather than
     row 1's. Caret position is carried as well, since restoring focus to a text
     field while dropping the cursor to the end is its own small bug. */
  function captureFocus() {
    const node = document.activeElement;
    if (!node || !node.dataset) return null;
    const key = node.dataset.focusKey;
    if (!key || !host().contains(node)) return null;

    const peers = host().querySelectorAll(`[data-focus-key="${CSS.escape(key)}"]`);
    const snap = { key, nth: Array.prototype.indexOf.call(peers, node) };
    if (typeof node.selectionStart === "number") {
      snap.start = node.selectionStart;
      snap.end = node.selectionEnd;
    }
    return snap;
  }

  function restoreFocus(snap) {
    if (!snap) return;
    const peers = host().querySelectorAll(`[data-focus-key="${CSS.escape(snap.key)}"]`);
    let node = peers[snap.nth] || peers[0];
    // Paging to the last page disables Next, and focusing a disabled button is
    // silently ignored — which would drop focus on <body> exactly where the
    // reader was working. Hand it to the sibling that is still operable.
    if (node && node.disabled) {
      node = node.parentElement ? node.parentElement.querySelector("[data-focus-key]:not([disabled])") : null;
    }
    if (!node) return;
    node.focus();
    if (snap.start !== undefined && typeof node.setSelectionRange === "function") {
      try { node.setSelectionRange(snap.start, snap.end); } catch (_) { /* type does not support a caret */ }
    }
  }

  async function view(descriptor, load, build) {
    const ownership = viewOwner.begin();
    const snap = captureFocus();
    const page = host();
    // The skeleton spans carry no text, so without this the region reads as
    // empty and then silently fills. aria-busy is the whole of the fix: the
    // route announcer already names the view, and a second live region firing
    // per load would talk over it.
    page.setAttribute("aria-busy", "true");
    render(pageSkeleton(descriptor));
    try {
      const data = await load(ownership.signal);
      if (!ownership.owns()) return;
      render(build(data));
    } catch (err) {
      if (!ownership.owns() || isAbort(err)) return;
      render(el("div", { class: `page${descriptor.narrow ? " page--narrow" : ""}` }, el("div", { class: "card" },
        emptyState("alert", "Could not load this view", err.message,
          el("button", { class: "btn btn--secondary", type: "button", onclick: () => route() }, icon("refresh"), "Try again")))));
    } finally {
      if (!ownership.owns()) return;
      page.removeAttribute("aria-busy");
      // Also runs on the error path, where nothing carries a focus key and this
      // is a no-op — cheaper than duplicating the call in both branches.
      restoreFocus(snap);
    }
  }

  const feedGlyph = (t) => (t === "project" ? "folder" : t === "team" ? "team" : t === "organization" ? "building" : "users");

  function feedList(events) {
    const wrap = el("ul", { class: "feed" });
    for (const e of events) {
      const name = (e.metadata && e.metadata.name) || "";
      wrap.append(el("li", { class: "feed__item" },
        el("span", { class: "feed__icon" }, icon(feedGlyph(e.target_type))),
        el("div", { class: "feed__body" },
          el("p", { class: "feed__title", text: `${fmt.title(e.target_type)} ${e.verb}` }),
          el("p", { class: "feed__meta", text: [name, fmt.ago(e.created_at)].filter(Boolean).join(" · ") }))));
    }
    return wrap;
  }

  /* --- Overview --- */

  pages.overview = () => view({
    title: `Good to see you, ${(state.profile.full_name || "there").split(" ")[0]}`,
    desc: `${state.profile.organization.name} · you hold the ${state.profile.roles[0] || "member"} role.`,
    action: "Refresh",
    content: el("div", { class: "stack" }, kpiSkeleton(), el("div", { class: "card" }, tableSkeleton(3, 4))),
  },
    async (signal) => {
      const [usage, activity] = await Promise.all([
        honestLoad(can("billing:view"), () => call("/billing/usage", { signal })),
        honestLoad(can("org:view"), () => list("/activity?page_size=7", { signal })),
      ]);
      return { usage, activity };
    },
    ({ usage, activity }) => {
      const org = state.profile.organization;
      const first = (state.profile.full_name || "there").split(" ")[0];

      const page = el("div", { class: "page" },
        pageHead(`Good to see you, ${first}`, `${org.name} · you hold the ${state.profile.roles[0] || "member"} role.`,
          el("button", { class: "btn btn--secondary", type: "button", onclick: () => route() }, icon("refresh"), "Refresh")));

      const kpis = el("div", { class: "kpis" });
      if (usage.kind === "ready") {
        const usageData = usage.value;
        kpis.append(
          kpi({ label: "Seats", value: usageData.seats, glyph: "users", meter: { used: usageData.seats, max: usageData.max_seats } }),
          kpi({ label: "Projects", value: usageData.projects, glyph: "folder", meter: { used: usageData.projects, max: usageData.max_projects } }),
          kpi({ label: "Teams", value: usageData.teams, glyph: "team", foot: "No plan limit" }),
          kpi({ label: "Plan", value: fmt.title(usageData.plan_code), glyph: "card", foot: "Current subscription" }));
      } else {
        kpis.append(
          kpi({ label: "Workspace", value: org.slug, glyph: "building", foot: "Tenant identifier" }),
          kpi({ label: "Your role", value: fmt.title(state.profile.roles[0] || "member"), glyph: "shield", foot: "Assigned access" }),
          kpi({ label: "Plan", value: fmt.title(org.plan_code || "free"), glyph: "card", foot: "Current subscription" }),
          kpi({ label: "Member since", value: fmt.date(state.profile.created_at), glyph: "clock", foot: "Account created" }));
      }

      let feed = null;
      if (activity.kind !== "forbidden") {
        feed = el("section", { class: "card" },
          el("div", { class: "card__head" },
            el("div", {}, el("h2", { class: "card__title", text: "Recent activity" }),
              el("p", { class: "card__sub", text: activity.kind === "ready" && activity.value.page
                ? `Showing latest ${activity.value.items.length} of ${activity.value.page.total}`
                : "What changed across the workspace" })),
            can("audit:view") ? el("a", { class: "link", href: "#/audit" }, "Audit log") : null));

        if (activity.kind === "error") {
          feed.append(el("div", { class: "card__body" }, inlineState("Activity unavailable", activity.error.message, () => refreshRoute("overview"))));
        } else {
          feed.append(activity.value.items.length
            ? el("div", { class: "card__body" }, feedList(activity.value.items))
            : emptyState("activity", "No activity yet", "Create a project or invite a teammate and it will appear here."));
        }
      }

      const detail = el("section", { class: "card" },
        el("div", { class: "card__head" }, el("div", {}, el("h2", { class: "card__title", text: "Session" }))),
        el("div", { class: "card__body" },
          el("dl", { class: "dl" },
            el("div", {}, el("dt", { text: "Organization" }), el("dd", { text: org.name })),
            el("div", {}, el("dt", { text: "Workspace" }), el("dd", { class: "mono", text: org.slug })),
            el("div", {}, el("dt", { text: "Email" }), el("dd", { text: state.profile.email })),
            el("div", {}, el("dt", { text: "Roles" }), el("dd", { text: state.profile.roles.map(fmt.title).join(", ") || "—" })),
            el("div", {}, el("dt", { text: "Email verified" }), el("dd", {}, state.profile.email_verified_at ? badge("Verified", "ok") : badge("Unverified", "warn"))),
            el("div", {}, el("dt", { text: "Permissions" }), el("dd", { text: String(state.profile.permissions.length) })))));

      const overviewStack = el("div", { class: "stack" }, kpis);
      if (usage.kind === "error") {
        overviewStack.append(el("section", { class: "card" }, el("div", { class: "card__body" },
          inlineState("Usage unavailable", usage.error.message, () => refreshRoute("overview")))));
      }
      overviewStack.append(el("div", { class: "split" }, feed, detail));
      page.append(overviewStack);
      return page;
    });

  /* --- Projects --- */

  // COLLECTION_CREATE_ACTIONS_BEGIN
  function collectionCreateActions(hasItems, filtered, allowed, create) {
    return {
      header: allowed && hasItems ? create() : null,
      empty: allowed && !hasItems && !filtered ? create() : null,
    };
  }
  // COLLECTION_CREATE_ACTIONS_END

  const q = {
    projects: { page: 1, search: "", status: "" },
    teams: { page: 1, search: "" },
    members: { page: 1, invitePage: 1 },
    apiKeys: { page: 1 },
    notifications: { page: 1 },
    audit: { page: 1, action: "" },
  };

  pages.projects = () => view({
    title: "Projects", desc: "Group work into projects and control who can access each one.",
    action: can("project:create") ? "New project" : null,
    content: el("div", { class: "card" }, tableSkeleton(6)),
  },
    async (signal) => {
      const p = new URLSearchParams({ page: String(q.projects.page), page_size: "10" });
      if (q.projects.search) p.set("search", q.projects.search);
      if (q.projects.status) p.set("status", q.projects.status);
      const [projects, teams] = await Promise.all([
        list(`/projects?${p}`, { signal }),
        can("team:view") ? fetchAllPages("/teams", { signal }) : { items: [] },
      ]);
      return { projects, teams: teams.items };
    },
    ({ projects, teams }) => {
      const create = () => el("button", { class: "btn btn--primary", type: "button", onclick: () => projectForm(null, teams) }, icon("plus"), "New project");
      const createActions = collectionCreateActions(projects.items.length > 0, Boolean(q.projects.search || q.projects.status), can("project:create"), create);
      const page = el("div", { class: "page" },
        pageHead("Projects", "Group work into projects and control who can access each one.", createActions.header));

      const card = el("section", { class: "card" },
        el("div", { class: "toolbar" },
          searchBox(q.projects.search, "Search projects…", (v) => { q.projects.search = v; q.projects.page = 1; refreshRoute("projects"); }),
          segment([
            { value: "", label: "All" }, { value: "active", label: "Active" }, { value: "archived", label: "Archived" },
          ], q.projects.status, (v) => { q.projects.status = v; q.projects.page = 1; refreshRoute("projects"); }, "Filter projects by status")));

      if (!projects.items.length) {
        card.append(emptyState("folder", q.projects.search || q.projects.status ? "No matching projects" : "No projects yet",
          q.projects.search || q.projects.status ? "Try clearing the filters." : "Projects are where work lives. Create the first one to get started.",
          createActions.empty));
      } else {
        const rows = projects.items.map((p) => {
          const actions = el("div", { class: "row-actions" });
          actions.append(rowBtn("users", "Members", () => membersSheet("project", p)));
          if (can("project:manage")) {
            actions.append(rowBtn("edit", "Edit", () => projectForm(p, teams)));
            const archiving = p.status !== "archived";
            actions.append(rowBtn(archiving ? "archive" : "refresh", archiving ? "Archive" : "Restore", async () => {
              try {
                await call(`/projects/${p.id}`, { method: "PATCH", body: { status: archiving ? "archived" : "active" } });
                toast(archiving ? "Project archived" : "Project restored", "ok");
                refreshRoute("projects");
              } catch (e) { if (shouldSurfaceAsyncError(e)) toast(e.message, "err"); }
            }, false, "archive-toggle"));
          }
          if (can("project:delete")) {
            actions.append(rowBtn("trash", "Delete", () => confirmModal({
              title: "Delete project?",
              desc: `“${p.name}” will be removed from the workspace. This cannot be undone from the dashboard.`,
              confirm: "Delete project",
              onConfirm: async () => {
                await call(`/projects/${p.id}`, { method: "DELETE" });
                toast("Project deleted", "ok");
                refreshRoute("projects");
              },
            }), true));
          }

          return el("tr", {},
            el("td", {}, el("div", { class: "cell" }, el("span", { class: "cell__main", text: p.name }), el("span", { class: "cell__sub", text: p.slug }))),
            el("td", {}, statusBadge(p.status)),
            el("td", {}, el("span", { class: "cell--muted", text: p.team_name || "—" })),
            el("td", { class: "num" }, String(p.member_count)),
            el("td", {}, el("span", { class: "cell--muted", text: fmt.date(p.created_at) })),
            el("td", { class: "actions" }, actions));
        });

        card.append(table([
          { label: "Project" }, { label: "Status" }, { label: "Team" },
          { label: "Members", num: true }, { label: "Created" }, { actions: true },
        ], rows, "Projects"));
        const p = pager(projects.page, (n) => { q.projects.page = n; refreshRoute("projects"); });
        if (p) card.append(p);
      }

      page.append(card);
      return page;
    });

  function projectForm(project, teams) {
    const edit = Boolean(project);
    formModal({
      title: edit ? "Edit project" : "New project",
      desc: edit ? "Update the project details." : "Projects group related work and carry their own membership.",
      submit: edit ? "Save changes" : "Create project",
      fields: [
        { name: "name", label: "Name", required: true, value: edit ? project.name : "", placeholder: "Website redesign" },
        { name: "slug", label: "Slug", optional: true, value: edit ? project.slug : "", hint: "Derived from the name when left blank" },
        { name: "description", label: "Description", type: "textarea", value: edit ? project.description : "" },
        {
          name: "team_id", label: "Owning team", type: "select",
          value: edit && project.team_id ? project.team_id : "",
          options: [{ value: "", label: "No team" }].concat(teams.map((t) => ({ value: t.id, label: t.name }))),
        },
      ],
      onSubmit: async (v) => {
        const body = { name: v.name, description: v.description };
        if (v.slug) body.slug = v.slug;
        if (v.team_id) body.team_id = v.team_id;
        else if (edit) body.clear_team = true;

        if (edit) {
          await call(`/projects/${project.id}`, { method: "PATCH", body });
          toast("Project updated", "ok");
        } else {
          await call("/projects", { method: "POST", body });
          toast("Project created", "ok");
        }
        refreshRoute("projects");
      },
    });
  }

  /* --- Teams --- */

  pages.teams = () => view({
    title: "Teams", desc: "Teams group people. Access itself comes from roles, not team membership.",
    action: can("team:create") ? "New team" : null,
    content: el("div", { class: "card" }, tableSkeleton(5)),
  },
    (signal) => {
      const p = new URLSearchParams({ page: String(q.teams.page), page_size: "10" });
      if (q.teams.search) p.set("search", q.teams.search);
      return list(`/teams?${p}`, { signal });
    },
    (teams) => {
      const create = () => el("button", { class: "btn btn--primary", type: "button", onclick: () => teamForm(null) }, icon("plus"), "New team");
      const createActions = collectionCreateActions(teams.items.length > 0, Boolean(q.teams.search), can("team:create"), create);
      const page = el("div", { class: "page" },
        pageHead("Teams", "Teams group people. Access itself comes from roles, not team membership.", createActions.header));

      const card = el("section", { class: "card" },
        el("div", { class: "toolbar" },
          searchBox(q.teams.search, "Search teams…", (v) => { q.teams.search = v; q.teams.page = 1; refreshRoute("teams"); })));

      if (!teams.items.length) {
        card.append(emptyState("team", q.teams.search ? "No matching teams" : "No teams yet",
          q.teams.search ? "Try a different search." : "Create a team to group people around a shared area of work.",
          createActions.empty));
      } else {
        const rows = teams.items.map((t) => {
          const actions = el("div", { class: "row-actions" });
          actions.append(rowBtn("users", "Members", () => membersSheet("team", t)));
          if (can("team:manage")) {
            actions.append(rowBtn("edit", "Edit", () => teamForm(t)));
            actions.append(rowBtn("trash", "Delete", () => confirmModal({
              title: "Delete team?",
              desc: `“${t.name}” will be removed. Projects it owns are kept and simply lose their team.`,
              confirm: "Delete team",
              onConfirm: async () => {
                await call(`/teams/${t.id}`, { method: "DELETE" });
                toast("Team deleted", "ok");
                refreshRoute("teams");
              },
            }), true));
          }
          return el("tr", {},
            el("td", {}, el("div", { class: "cell" }, el("span", { class: "cell__main", text: t.name }), el("span", { class: "cell__sub", text: t.slug }))),
            el("td", {}, el("span", { class: "cell--muted", text: t.description || "—" })),
            el("td", { class: "num" }, String(t.member_count)),
            el("td", {}, el("span", { class: "cell--muted", text: fmt.date(t.created_at) })),
            el("td", { class: "actions" }, actions));
        });

        card.append(table([
          { label: "Team" }, { label: "Description" }, { label: "Members", num: true }, { label: "Created" }, { actions: true },
        ], rows, "Teams"));
        const p = pager(teams.page, (n) => { q.teams.page = n; refreshRoute("teams"); });
        if (p) card.append(p);
      }

      page.append(card);
      return page;
    });

  function teamForm(team) {
    const edit = Boolean(team);
    formModal({
      title: edit ? "Edit team" : "New team",
      desc: edit ? "Update the team details." : "Name the team; the slug is derived automatically.",
      submit: edit ? "Save changes" : "Create team",
      fields: [
        { name: "name", label: "Name", required: true, value: edit ? team.name : "", placeholder: "Platform Engineering" },
        { name: "slug", label: "Slug", optional: true, value: edit ? team.slug : "", hint: "Derived from the name when left blank" },
        { name: "description", label: "Description", type: "textarea", value: edit ? team.description : "" },
      ],
      onSubmit: async (v) => {
        const body = { name: v.name, description: v.description };
        if (v.slug) body.slug = v.slug;
        if (edit) {
          await call(`/teams/${team.id}`, { method: "PATCH", body });
          toast("Team updated", "ok");
        } else {
          await call("/teams", { method: "POST", body });
          toast("Team created", "ok");
        }
        refreshRoute("teams");
      },
    });
  }

  /** membersSheet manages the roster of a team or project in a slide-over. */
  async function membersSheet(kind, resource) {
    const ownership = actionOwner.begin();
    const base = kind === "team" ? "teams" : "projects";
    const perm = kind === "team" ? "team:manage" : "project:manage";

    let members = [];
    let people = [];
    try {
      const [m, u] = await Promise.all([
        fetchAllPages(`/${base}/${resource.id}/members`, { signal: ownership.signal }),
        can("member:view") ? fetchAllPages("/users", { signal: ownership.signal }) : { items: [] },
      ]);
      members = m.items;
      people = u.items;
    } catch (err) {
      if (ownership.owns() && !isAbort(err)) toast(err.message, "err");
      return;
    }
    if (!ownership.owns()) return;

    const have = new Set(members.map((m) => m.user_id));
    const available = people.filter((p) => !have.has(p.id));

    const roster = members.length ? el("ul", { class: "stack" }) : el("div", { class: "stack" });
    if (!members.length) {
      roster.append(emptyState("users", "No members yet", `Nobody has been added to ${resource.name}.`));
    } else {
      for (const m of members) {
        roster.append(el("li", { class: "feed__item" },
          el("span", { class: "avatar avatar--md", text: fmt.initials(m.full_name, m.email) }),
          el("div", { class: "feed__body" },
            el("p", { class: "feed__title", text: m.full_name || m.email }),
            el("p", { class: "feed__meta", text: m.email })),
          can(perm) ? rowBtn("x", `Remove ${m.email}`, async () => {
            try {
              await call(`/${base}/${resource.id}/members/${m.user_id}`, { method: "DELETE" });
              toast("Member removed", "ok");
              closeModal();
              refreshRoute(kind === "team" ? "teams" : "projects");
            } catch (e) { if (shouldSurfaceAsyncError(e, ownership)) toast(e.message, "err"); }
          }, true) : null));
      }
    }

    const body = el("div", { class: "modal__body" }, roster);

    if (can(perm)) {
      if (!available.length) {
        body.append(el("p", { class: "field__hint", text: "Everyone in the organization is already a member." }));
      } else {
        // The visible text is now the accessible name. It was an orphan <label>
        // beside a select named "Choose someone to add", so a speech-input user
        // saying "Add a member" addressed a control that answered to something
        // else (2.5.3 Label in Name).
        const select = el("select", { class: "select", id: "member-add" });
        for (const p of available) select.append(el("option", { value: p.id }, `${p.full_name || p.email} · ${p.email}`));

        const add = el("button", { class: "btn btn--primary btn--block", type: "button" }, icon("plus"), `Add to ${kind}`);
        add.addEventListener("click", async () => {
          const stopBusy = setButtonBusy(add, `Adding to ${kind}…`);
          try {
            await call(`/${base}/${resource.id}/members`, { method: "POST", body: { user_id: select.value } });
            toast("Member added", "ok");
            closeModal();
            refreshRoute(kind === "team" ? "teams" : "projects");
          } catch (e) {
            if (shouldSurfaceAsyncError(e, ownership)) toast(e.message, "err");
          } finally {
            if (ownership.owns()) stopBusy();
          }
        });

        body.append(el("hr", { class: "divider" }),
          el("div", { class: "field" }, el("label", { class: "field__label", for: "member-add", text: "Add a member" }), select), add);
      }
    }

    openModal({
      title: `${resource.name} members`,
      desc: "Membership groups people around this work. It grants no permissions by itself.",
      body, ownership,
    });
  }

  /* --- Members & invitations --- */

  pages.members = () => view({
    title: "Members", desc: "Everyone with access to this workspace, plus invitations still outstanding.",
    action: can("member:invite") && can("role:view") ? "Invite member" : null,
    content: el("div", { class: "stack" }, el("div", { class: "card" }, tableSkeleton(5)), el("div", { class: "card" }, tableSkeleton(4, 3))),
  },
    async (signal) => {
      const [users, invites, roles] = await Promise.all([
        list(`/users?page=${q.members.page}&page_size=10`, { signal }),
        honestLoad(can("member:view"), () => list(`/invitations?status=pending&page=${q.members.invitePage}&page_size=10`, { signal })),
        honestLoad(can("member:invite") && can("role:view"), () => call("/roles", { signal })),
      ]);
      return { users, invites, roles };
    },
    ({ users, invites, roles }) => {
      const roleItems = roles.kind === "ready" ? (roles.value || []) : [];
      const page = el("div", { class: "page" },
        pageHead("Members", "Everyone with access to this workspace, plus invitations still outstanding.",
          can("member:invite") && roles.kind === "ready" ? el("button", { class: "btn btn--primary", type: "button", onclick: () => inviteForm(roleItems) }, icon("mail"), "Invite member") : null));

      const dir = el("section", { class: "card" },
        el("div", { class: "card__head" }, el("div", {}, el("h2", { class: "card__title", text: "Directory" }),
          el("p", { class: "card__sub", text: `${users.page ? users.page.total : users.items.length} people` }))));

      const rows = users.items.map((u) => {
        return el("tr", {},
          el("td", {}, el("div", { class: "identity" },
            el("span", { class: "avatar avatar--md", text: fmt.initials(u.full_name, u.email) }),
            el("div", { class: "cell" }, el("span", { class: "cell__main", text: u.full_name || "—" }), el("span", { class: "cell__sub", text: u.email })))),
          el("td", {}, statusBadge(u.status)),
          el("td", {}, u.email_verified_at ? badge("Verified", "ok") : badge("Unverified", "warn")),
          el("td", {}, el("span", { class: "cell--muted", text: fmt.date(u.created_at) })),
          el("td", { class: "actions" }, u.id === state.profile.user_id ? badge("You", "brand") : null));
      });

      dir.append(table([
        { label: "Person" }, { label: "Status" }, { label: "Email" }, { label: "Joined" }, { actions: true },
      ], rows, "Member directory"));
      const pg = pager(users.page, (n) => { q.members.page = n; refreshRoute("members"); });
      if (pg) dir.append(pg);

      const stack = el("div", { class: "stack" }, dir);
      if (can("member:invite") && roles.kind === "error") {
        stack.append(el("section", { class: "card" }, el("div", { class: "card__body" },
          inlineState("Invitation roles unavailable", roles.error.message, () => refreshRoute("members")))));
      }

      if (can("member:view")) {
        const inviteItems = invites.kind === "ready" ? invites.value.items : [];
        const inv = el("section", { class: "card" },
          el("div", { class: "card__head" }, el("div", {},
            el("h2", { class: "card__title", text: "Pending invitations" }),
            el("p", { class: "card__sub", text: "Pending invitations count against the plan's seat limit" }))));

        if (invites.kind === "error") {
          inv.append(el("div", { class: "card__body" }, inlineState("Invitations unavailable", invites.error.message, () => refreshRoute("members"))));
        } else if (!inviteItems.length) {
          inv.append(emptyState("mail", "No pending invitations", "Everyone invited has responded."));
        } else {
          const irows = inviteItems.map((i) => {
            const actions = el("div", { class: "row-actions" });
            if (can("member:invite")) {
              actions.append(rowBtn("trash", "Revoke", () => confirmModal({
                title: "Revoke invitation?",
                desc: `The link sent to ${i.email} will stop working immediately.`,
                confirm: "Revoke invitation",
                onConfirm: async () => {
                  await call(`/invitations/${i.id}`, { method: "DELETE" });
                  toast("Invitation revoked", "ok");
                  refreshRoute("members");
                },
              }), true));
            }
            return el("tr", {},
              el("td", {}, el("span", { class: "mono", text: i.email })),
              el("td", {}, badge(i.role_name || "Member", "brand")),
              el("td", {}, el("span", { class: "cell--muted", text: `Expires ${fmt.date(i.expires_at)}` })),
              el("td", { class: "actions" }, actions));
          });
          inv.append(table([{ label: "Email" }, { label: "Role" }, { label: "Expiry" }, { actions: true }], irows, "Pending invitations"));
          const invitePager = pager(invites.value.page, (n) => { q.members.invitePage = n; refreshRoute("members"); });
          if (invitePager) inv.append(invitePager);
        }
        stack.append(inv);
      }

      page.append(stack);
      return page;
    });

  function inviteForm(roles) {
    formModal({
      title: "Invite a member",
      desc: "They receive a single-use link. Only its hash is stored, so it cannot be retrieved later.",
      submit: "Send invitation",
      fields: [
        { name: "email", label: "Email address", type: "email", required: true, placeholder: "teammate@company.com" },
        { name: "role_id", label: "Role", type: "select", required: true, options: roles.map((r) => ({ value: r.id, label: r.name })) },
      ],
      onSubmit: async (v, ownership) => {
        const res = await call("/invitations", { method: "POST", body: { email: v.email, role_id: v.role_id } });
        refreshRoute("members");
        setTimeout(() => { if (ownership.owns()) showInvite(v.email, res.token, ownership); }, 140);
      },
    });
  }

  function showInvite(email, token, ownership) {
    const link = `${window.location.origin}/app?invite=${encodeURIComponent(token)}`;
    openModal({
      title: "Invitation created",
      desc: `Send this single-use link to ${email}. It is shown once and cannot be retrieved again.`,
      body: el("div", { class: "modal__body" },
        el("div", { class: "secret" },
          el("code", { text: link }),
          el("button", { class: "icon-btn", type: "button", title: "Copy link", "aria-label": "Copy invitation link", onclick: () => copy(link) }, icon("copy"))),
        el("p", { class: "field__hint", text: "The invitation was also emailed. This copy is shown once in case delivery fails or the address was mistyped." })),
      footer: el("div", { class: "modal__foot" }, el("button", { class: "btn btn--primary", type: "button", onclick: closeModal }, "Done")),
      ownership,
    });
  }

  /* --- Roles --- */

  pages.roles = () => view({
    title: "Roles", desc: "Permissions resolve from the database on every request, so edits take effect immediately.",
    action: can("role:manage") ? "New role" : null,
    content: el("div", { class: "card" }, tableSkeleton(5)),
  },
    async (signal) => {
      const [roles, catalog] = await Promise.all([call("/roles", { signal }), call("/permissions", { signal })]);
      return { roles: roles || [], catalog: catalog || [] };
    },
    ({ roles, catalog }) => {
      const page = el("div", { class: "page" },
        pageHead("Roles", "Permissions resolve from the database on every request, so edits take effect immediately.",
          can("role:manage") ? el("button", { class: "btn btn--primary", type: "button", onclick: () => roleForm(catalog) }, icon("plus"), "New role") : null));

      const rows = roles.map((r) => {
        const actions = el("div", { class: "row-actions" });
        if (can("role:manage")) {
          actions.append(rowBtn("edit", "Edit permissions", () => rolePerms(r)));
          // System roles are never deletable: registration and several
          // authorization flows assume owner/admin/member exist.
          if (!r.is_system) {
            actions.append(rowBtn("trash", "Delete", () => confirmModal({
              title: "Delete role?",
              desc: `“${r.name}” will be removed. Anyone holding only this role loses its permissions.`,
              confirm: "Delete role",
              onConfirm: async () => {
                await call(`/roles/${r.id}`, { method: "DELETE" });
                toast("Role deleted", "ok");
                refreshRoute("roles");
              },
            }), true));
          }
        }
        return el("tr", {},
          el("td", {}, el("div", { class: "cell" }, el("span", { class: "cell__main", text: r.name }), el("span", { class: "cell__sub", text: r.slug }))),
          el("td", {}, el("span", { class: "cell--muted", text: r.description || "—" })),
          el("td", {}, r.is_system ? badge("System") : badge("Custom", "brand")),
          el("td", { class: "num" }, String(r.rank)),
          el("td", { class: "actions" }, actions));
      });

      const card = el("section", { class: "card" }, table([
        { label: "Role" }, { label: "Description" }, { label: "Type" }, { label: "Rank", num: true }, { actions: true },
      ], rows, "Roles"));

      const cat = el("section", { class: "card" },
        el("div", { class: "card__head" }, el("div", {},
          el("h2", { class: "card__title", text: "Permission catalog" }),
          el("p", { class: "card__sub", text: "The platform-defined capabilities roles are composed from" }))),
        el("div", { class: "card__body" }, el("div", { class: "badges" }, catalog.map((p) => badge(p.code)))));

      page.append(el("div", { class: "stack" }, card, cat));
      return page;
    });

  function roleForm(catalog) {
    formModal({
      title: "New role",
      desc: "Create a custom role and choose exactly what it grants.",
      submit: "Create role",
      wide: true,
      fields: [
        { name: "name", label: "Name", required: true, placeholder: "Release Manager" },
        { name: "description", label: "Description", type: "textarea" },
        { name: "permission_codes", label: "Permissions", type: "picker", options: catalog, value: [] },
      ],
      onSubmit: async (v) => {
        await call("/roles", { method: "POST", body: { name: v.name, description: v.description, permission_codes: v.permission_codes } });
        toast("Role created", "ok");
        refreshRoute("roles");
      },
    });
  }

  // ROLE_PERMISSION_EDITOR_VALIDATION_BEGIN
  function validateRolePermissionEditorData(role, current, freshCatalog) {
    if (!current || current.role_id !== role.id ||
        !Array.isArray(current.permission_codes) ||
        typeof current.revision !== "string" || current.revision.length === 0 ||
        !Array.isArray(freshCatalog)) {
      throw new Error("the server returned incomplete or mismatched role data");
    }

    const catalogCodes = new Set();
    for (const permission of freshCatalog) {
      if (!permission || typeof permission.code !== "string" || permission.code.length === 0 ||
          catalogCodes.has(permission.code)) {
        throw new Error("the permission catalog cannot be represented exactly");
      }
      catalogCodes.add(permission.code);
    }

    const grantCodes = new Set();
    for (const code of current.permission_codes) {
      if (typeof code !== "string" || code.length === 0 || grantCodes.has(code) || !catalogCodes.has(code)) {
        throw new Error("the role's current grants cannot be represented exactly by the permission catalog");
      }
      grantCodes.add(code);
    }

    return { initialCodes: current.permission_codes.slice(), catalog: freshCatalog };
  }
  // ROLE_PERMISSION_EDITOR_VALIDATION_END

  async function rolePerms(role) {
    const ownership = actionOwner.begin();
    let current;
    let editorData;
    try {
      const freshCatalogRequest = call("/permissions", { signal: ownership.signal });
      [current, editorData] = await Promise.all([
        call(`/roles/${role.id}/permissions`, { signal: ownership.signal }),
        freshCatalogRequest,
      ]);
      editorData = validateRolePermissionEditorData(role, current, editorData);
    } catch (err) {
      if (ownership.owns() && !isAbort(err)) toast(`Could not load ${role.name} permissions: ${err.message}. The editor was not opened.`, "err");
      return false;
    }
    if (!ownership.owns()) return false;

    const initialCodes = editorData.initialCodes;
    formModal({
      title: `${role.name} permissions`,
      desc: role.is_system
        ? "This is a system role: its permission set is editable, but it cannot be renamed or deleted."
        : "Choose exactly which capabilities this role grants.",
      submit: "Save permissions",
      submitWhenChanged: true,
      wide: true,
      fields: [{ name: "permission_codes", label: "Permissions", type: "picker", options: editorData.catalog, value: initialCodes }],
      ownership,
      onSubmit: async (v) => {
        if (initialCodes.length > 0 && v.permission_codes.length === 0 &&
            !window.confirm(`Remove every permission from ${role.name}? Members with only this role will lose all access it grants.`)) {
          throw new ApiError(0, "CANCELED", "Remove-all was canceled. No permissions were changed.");
        }
        try {
          await call(`/roles/${role.id}/permissions`, {
            method: "PUT",
            headers: { "If-Match": `"${current.revision}"` },
            body: { permission_codes: v.permission_codes },
          });
        } catch (err) {
          if (err.status === 409) {
            const conflict = new ApiError(409, "CONFLICT", "This role changed after you opened the editor. Your selection was not saved.");
            conflict.actionLabel = "Reload permissions";
            conflict.onAction = async () => {
              if (!await rolePerms(role)) throw new Error("The latest permissions could not be loaded.");
            };
            throw conflict;
          }
          throw err;
        }
        toast("Permissions updated", "ok");
        refreshRoute("roles");
      },
    });
    return true;
  }

  /* --- API keys --- */

  // Mirrors authz.GrantableScopes(): role and organization administration are
  // excluded server-side, so the picker must not offer them.
  const SCOPES = [
    "org:view", "member:view", "member:invite", "role:view",
    "team:view", "team:create", "team:manage",
    "project:view", "project:create", "project:manage", "project:delete",
    "apikey:view", "billing:view", "file:upload", "file:delete", "audit:view",
  ];

  pages["api-keys"] = () => view({
    title: "API keys", desc: "Machine credentials for server-to-server access, limited to the scopes you choose.",
    action: can("apikey:manage") ? "Create key" : null,
    content: el("div", { class: "card" }, tableSkeleton(6)),
  },
    (signal) => list(`/api-keys?page=${q.apiKeys.page}&page_size=20`, { signal }),
    (keys) => {
      const create = () => el("button", { class: "btn btn--primary", type: "button", onclick: keyForm }, icon("plus"), "Create key");
      const createActions = collectionCreateActions(keys.items.length > 0, false, can("apikey:manage"), create);
      const page = el("div", { class: "page" },
        pageHead("API keys", "Machine credentials for server-to-server access, limited to the scopes you choose.", createActions.header));

      const card = el("section", { class: "card" });

      if (!keys.items.length) {
        card.append(emptyState("key", "No API keys",
          "Create a key so a CI pipeline or service can call the API without a user session.",
          createActions.empty));
      } else {
        const rows = keys.items.map((k) => {
          const revoked = Boolean(k.revoked_at);
          const expired = k.expires_at && new Date(k.expires_at) < new Date();
          const actions = el("div", { class: "row-actions" });
          if (can("apikey:manage") && !revoked) {
            actions.append(rowBtn("trash", "Revoke", () => confirmModal({
              title: "Revoke API key?",
              desc: `“${k.name}” stops working immediately. Any service using it will start receiving 401 responses.`,
              confirm: "Revoke key",
              onConfirm: async () => {
                await call(`/api-keys/${k.id}`, { method: "DELETE" });
                toast("API key revoked", "ok");
                refreshRoute("api-keys");
              },
            }), true));
          }
          return el("tr", {},
            el("td", {}, el("div", { class: "cell" }, el("span", { class: "cell__main", text: k.name }), el("span", { class: "cell__sub mono", text: `${k.key_prefix}…` }))),
            el("td", {}, el("div", { class: "badges" }, (k.scopes || []).map((s) => badge(s)))),
            el("td", {}, revoked ? badge("Revoked", "err") : expired ? badge("Expired") : badge("Active", "ok")),
            el("td", {}, el("span", { class: "cell--muted", text: k.last_used_at ? fmt.ago(k.last_used_at) : "Never used" })),
            el("td", {}, el("span", { class: "cell--muted", text: k.expires_at ? fmt.date(k.expires_at) : "No expiry" })),
            el("td", { class: "actions" }, actions));
        });

        card.append(table([
          { label: "Key" }, { label: "Scopes" }, { label: "State" }, { label: "Last used" }, { label: "Expires" }, { actions: true },
        ], rows, "API keys"));
        const keyPager = pager(keys.page, (n) => { q.apiKeys.page = n; refreshRoute("api-keys"); });
        if (keyPager) card.append(keyPager);
      }

      page.append(card);
      return page;
    });

  function keyForm() {
    formModal({
      title: "Create API key",
      desc: "The secret is shown once on creation and stored only as a hash.",
      submit: "Create key",
      wide: true,
      fields: [
        { name: "name", label: "Name", required: true, placeholder: "CI pipeline" },
        { name: "expires_at", label: "Expires on", type: "date", optional: true, hint: "Recommended — an unexpiring key that leaks is valid until noticed" },
        { name: "scopes", label: "Scopes", type: "picker", options: SCOPES.map((code) => ({ code })), value: ["project:view"] },
      ],
      onSubmit: async (v, ownership) => {
        if (!v.scopes.length) throw new ApiError(400, "VALIDATION", "Choose at least one scope.");
        const body = { name: v.name, scopes: v.scopes };
        if (v.expires_at) body.expires_at = new Date(`${v.expires_at}T23:59:59Z`).toISOString();
        const res = await call("/api-keys", { method: "POST", body });
        refreshRoute("api-keys");
        setTimeout(() => { if (ownership.owns()) showSecret(res.secret, ownership); }, 140);
      },
    });
  }

  function showSecret(secret, ownership) {
    openModal({
      title: "Copy your API key",
      desc: "This is the only time the key is shown. Store it in your secret manager now.",
      body: el("div", { class: "modal__body" },
        el("div", { class: "secret" },
          el("code", { text: secret }),
          el("button", { class: "icon-btn", type: "button", title: "Copy key", "aria-label": "Copy API key", onclick: () => copy(secret) }, icon("copy"))),
        el("p", { class: "field__hint", text: "Send it as an X-API-Key header, or as Authorization: Bearer. If it leaks, revoke it here immediately." })),
      footer: el("div", { class: "modal__foot" }, el("button", { class: "btn btn--primary", type: "button", onclick: closeModal }, "I've stored it")),
      ownership,
    });
  }

  /* --- Billing --- */

  pages.billing = () => view({
    title: "Billing", desc: "Quota limits are enforced by the API, not merely displayed here.",
    action: can("billing:manage") ? "Cancel plan" : null,
    content: el("div", { class: "stack" }, kpiSkeleton(), el("div", { class: "card" }, tableSkeleton(3, 3))),
  },
    async (signal) => {
      const [plans, sub, usage] = await Promise.all([
        call("/billing/plans", { auth: false, signal }), call("/billing/subscription", { signal }), call("/billing/usage", { signal }),
      ]);
      return { plans: plans || [], sub, usage };
    },
    ({ plans, sub, usage }) => {
      const current = sub.plan;

      const cancel = can("billing:manage") && sub.subscription
        ? el("button", { class: "btn btn--danger-quiet", type: "button", onclick: () => confirmModal({
          title: "Cancel subscription?",
          desc: "Access continues until the end of the period already paid for.",
          confirm: "Cancel subscription",
          onConfirm: async () => {
            await call("/billing/subscription", { method: "DELETE" });
            toast("Subscription cancels at period end", "ok");
            refreshRoute("billing");
          },
        }) }, "Cancel plan")
        : null;

      const page = el("div", { class: "page" },
        pageHead("Billing", "Quota limits are enforced by the API, not merely displayed here.", cancel));

      const kpis = el("div", { class: "kpis" },
        kpi({ label: "Seats", value: usage.seats, glyph: "users", meter: { used: usage.seats, max: usage.max_seats } }),
        kpi({ label: "Projects", value: usage.projects, glyph: "folder", meter: { used: usage.projects, max: usage.max_projects } }),
        kpi({ label: "Teams", value: usage.teams, glyph: "team", foot: "No plan limit" }),
        kpi({ label: "Current plan", value: current.name, glyph: "card", foot: fmt.price(current) + (current.price_cents ? " / month" : "") }));

      const stack = el("div", { class: "stack" }, kpis);

      if (sub.subscription) {
        const s = sub.subscription;
        stack.append(el("section", { class: "card" },
          el("div", { class: "card__head" }, el("div", {}, el("h2", { class: "card__title", text: "Subscription" }))),
          el("div", { class: "card__body" }, el("dl", { class: "dl" },
            el("div", {}, el("dt", { text: "Status" }), el("dd", {}, statusBadge(s.status))),
            el("div", {}, el("dt", { text: "Period start" }), el("dd", { text: fmt.date(s.current_period_start) })),
            el("div", {}, el("dt", { text: "Renews" }), el("dd", { text: fmt.date(s.current_period_end) })),
            el("div", {}, el("dt", { text: "Cancels at period end" }), el("dd", { text: s.cancel_at_period_end ? "Yes" : "No" }))))));
      }

      const grid = el("div", { class: "plans" });
      for (const plan of plans) {
        const isCurrent = plan.code === current.code;
        let cta;
        if (isCurrent) cta = el("button", { class: "btn btn--secondary", type: "button", disabled: true }, "Current plan");
        else if (can("billing:manage")) {
          cta = el("button", { class: "btn btn--primary", type: "button", onclick: () => changePlan(plan) }, `Switch to ${plan.name}`);
        } else cta = null;

        grid.append(el("article", { class: `plan${isCurrent ? " plan--current" : ""}` },
          el("h3", { class: "plan__name" }, plan.name, isCurrent ? badge("Current", "brand") : null),
          el("p", { class: "plan__price" }, fmt.price(plan),
            plan.price_cents ? el("span", { class: "plan__per", text: `/${plan.billing_period === "yearly" ? "yr" : "mo"}` }) : null),
          el("ul", { class: "plan__list" },
            el("li", {}, icon("check", "icon icon--xs"), `${fmt.limit(plan.max_seats)} seats`),
            el("li", {}, icon("check", "icon icon--xs"), `${fmt.limit(plan.max_projects)} projects`),
            el("li", {}, icon("check", "icon icon--xs"), `${fmt.limit(plan.max_storage_mb)} MB storage`),
            el("li", {}, icon("check", "icon icon--xs"), plan.features && plan.features.api_access ? "API access" : "Dashboard only")),
          cta));
      }

      stack.append(el("section", { class: "card" },
        el("div", { class: "card__head" }, el("div", {}, el("h2", { class: "card__title", text: "Plans" }))),
        el("div", { class: "card__body" }, grid)));

      page.append(stack);
      return page;
    });

  function changePlan(plan) {
    confirmModal({
      title: `Switch to ${plan.name}?`,
      desc: "A downgrade is refused when current usage exceeds the target plan's limits; reduce usage first.",
      confirm: `Switch to ${plan.name}`,
      onConfirm: async () => {
        await call("/billing/subscription", { method: "POST", body: { plan_code: plan.code } });
        toast(`Now on the ${plan.name} plan`, "ok");
        await loadProfile();
        paintIdentity();
        refreshRoute("billing");
      },
    });
  }

  /* --- Audit --- */

  pages.audit = () => view({
    title: "Audit log", desc: "An append-only record of security-relevant actions. The application role holds no UPDATE or DELETE grant on this table.",
    content: el("div", { class: "card" }, tableSkeleton(4, 8)),
  },
    async (signal) => {
      const p = new URLSearchParams({ page: String(q.audit.page), page_size: "15" });
      if (q.audit.action) p.set("action", q.audit.action);
      const [logs, activity] = await Promise.all([
        list(`/audit-logs?${p}`, { signal }),
        honestLoad(can("org:view"), () => list("/activity?page_size=8", { signal })),
      ]);
      return { logs, activity };
    },
    ({ logs, activity }) => {
      const page = el("div", { class: "page" },
        pageHead("Audit log", "An append-only record of security-relevant actions. The application role holds no UPDATE or DELETE grant on this table."));

      const card = el("section", { class: "card" },
        el("div", { class: "toolbar" },
          searchBox(q.audit.action, "Filter by action, e.g. team.created", (v) => { q.audit.action = v; q.audit.page = 1; refreshRoute("audit"); })));

      if (!logs.items.length) {
        card.append(emptyState("activity", q.audit.action ? "No matching entries" : "No audit entries",
          q.audit.action ? "No entries match that action." : "Administrative actions are recorded here as they happen."));
      } else {
        const rows = logs.items.map((e) => {
          return el("tr", {},
            el("td", {}, el("span", { class: "mono", text: e.action })),
            el("td", {}, el("span", { class: "cell--muted", text: e.target_type ? fmt.title(e.target_type) : "—" })),
            el("td", {}, el("span", { class: "cell--mono", text: e.ip_address || "—" })),
            el("td", {}, el("span", { class: "cell--muted", text: fmt.dateTime(e.created_at) })));
        });
        card.append(table([{ label: "Action" }, { label: "Target" }, { label: "Source IP" }, { label: "When" }], rows, "Audit log entries"));
        const pg = pager(logs.page, (n) => { q.audit.page = n; refreshRoute("audit"); });
        if (pg) card.append(pg);
      }

      let feed = null;
      if (activity.kind !== "forbidden") {
        feed = el("section", { class: "card" },
          el("div", { class: "card__head" }, el("div", {},
            el("h2", { class: "card__title", text: "Activity" }),
            el("p", { class: "card__sub", text: activity.kind === "ready" && activity.value.page
              ? `Showing latest ${activity.value.items.length} of ${activity.value.page.total}`
              : "The product-facing counterpart to the audit trail" }))));
        if (activity.kind === "error") {
          feed.append(el("div", { class: "card__body" }, inlineState("Activity unavailable", activity.error.message, () => refreshRoute("audit"))));
        } else {
          feed.append(activity.value.items.length
            ? el("div", { class: "card__body" }, feedList(activity.value.items))
            : emptyState("activity", "Nothing yet", "Activity appears as people work in the workspace."));
        }
      }

      page.append(el("div", { class: "split" }, card, feed));
      return page;
    });

  /* --- Settings --- */

  pages.settings = () => view({
    title: "Settings", desc: "Your profile, display preferences, and password.", narrow: true,
    content: el("div", { class: "card" }, tableSkeleton(2, 5)),
  },
    async (signal) => {
      const [profile, prefs] = await Promise.all([call("/profile", { signal }), call("/preferences", { signal })]);
      return { profile, prefs };
    },
    ({ profile, prefs }) => {
      const page = el("div", { class: "page page--narrow" },
        pageHead("Settings", "Your profile, display preferences, and password."));

      /* Profile */
      const name = el("input", { class: "input", id: "s-name", value: profile.full_name, required: true, maxlength: 255 });
      const avatar = el("input", { class: "input", id: "s-avatar", type: "url", value: profile.avatar_url || "", placeholder: "https://…" });
      const saveProfile = el("button", { class: "btn btn--primary", type: "submit" }, "Save profile");

      const profileForm = el("form", {});
      profileForm.addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const stopBusy = setButtonBusy(saveProfile, "Saving…");
        try {
          const body = { full_name: name.value.trim() };
          if (avatar.value.trim()) body.avatar_url = avatar.value.trim();
          else body.clear_avatar = true;
          await call("/profile", { method: "PATCH", body });
          toast("Profile updated", "ok");
          await loadProfile();
          paintIdentity();
        } catch (e) { if (shouldSurfaceAsyncError(e)) toast(e.message, "err"); } finally { stopBusy(); }
      });
      profileForm.append(
        el("div", { class: "form-row" },
          el("div", {}, el("label", { class: "form-row__label", for: "s-name", text: "Full name" }), el("p", { class: "form-row__hint", text: "Shown to teammates across the workspace" })),
          el("div", {}, name)),
        el("div", { class: "form-row" },
          el("div", {}, el("label", { class: "form-row__label", for: "s-avatar", text: "Avatar URL" }), el("p", { class: "form-row__hint", text: "Must be an absolute https URL" })),
          el("div", {}, avatar)),
        el("div", { class: "form-row" },
          // Disabled, but still labelled: a reader navigating the form is told
          // what the field is before being told it cannot be edited.
          el("div", {}, el("label", { class: "form-row__label", for: "s-email", text: "Email" }), el("p", { class: "form-row__hint", text: "Contact an administrator to change this" })),
          el("div", {}, el("input", { class: "input", id: "s-email", type: "email", value: profile.email, disabled: true }))),
        el("div", { class: "form-actions" }, saveProfile));

      /* Preferences */
      const themeSel = el("select", { class: "select", id: "s-theme" });
      for (const t of ["dark", "light", "system"]) {
        themeSel.append(el("option", { value: t, selected: theme.current() === t }, fmt.title(t)));
      }
      const tz = el("input", { class: "input", id: "s-tz", value: prefs.timezone, maxlength: 50 });
      const locale = el("input", { class: "input", id: "s-locale", value: prefs.locale, maxlength: 10 });
      const mails = el("input", { type: "checkbox", id: "s-mails" });
      mails.checked = prefs.email_notifications;
      const savePrefs = el("button", { class: "btn btn--primary", type: "submit" }, "Save preferences");

      // Theme applies instantly on change: a display preference that waits for a
      // round trip feels broken.
      themeSel.addEventListener("change", () => theme.apply(themeSel.value));

      const prefsForm = el("form", {});
      prefsForm.addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const stopBusy = setButtonBusy(savePrefs, "Saving…");
        try {
          await call("/preferences", {
            method: "PATCH",
            body: { theme: themeSel.value, timezone: tz.value.trim(), locale: locale.value.trim(), email_notifications: mails.checked },
          });
          theme.apply(themeSel.value);
          toast("Preferences saved", "ok");
        } catch (e) { if (shouldSurfaceAsyncError(e)) toast(e.message, "err"); } finally { stopBusy(); }
      });
      prefsForm.append(
        el("div", { class: "form-row" },
          el("div", {}, el("label", { class: "form-row__label", for: "s-theme", text: "Theme" }), el("p", { class: "form-row__hint", text: "System follows your operating system" })),
          el("div", {}, themeSel)),
        el("div", { class: "form-row" },
          el("div", {}, el("label", { class: "form-row__label", for: "s-tz", text: "Timezone" })),
          el("div", {}, tz)),
        el("div", { class: "form-row" },
          el("div", {}, el("label", { class: "form-row__label", for: "s-locale", text: "Locale" })),
          el("div", {}, locale)),
        el("div", { class: "form-row" },
          // Stays a <p>: the checkbox already has its own label ("Enabled"), and
          // a second `for` pointing at it would concatenate into the name.
          el("div", {}, el("p", { class: "form-row__label", text: "Email notifications" }), el("p", { class: "form-row__hint", text: "Receive email for important workspace events" })),
          el("div", {}, el("div", { class: "check" }, mails, el("label", { class: "check__label", for: "s-mails", text: "Enabled" })))),
        el("div", { class: "form-actions" }, savePrefs));

      /* Password */
      const cur = el("input", { class: "input", id: "s-cur", type: "password", autocomplete: "current-password", required: true });
      const nw = el("input", { class: "input", id: "s-new", type: "password", autocomplete: "new-password", required: true, minlength: 8 });
      const savePw = el("button", { class: "btn btn--primary", type: "submit" }, "Change password");

      const pwForm = el("form", {});
      pwForm.addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const stopBusy = setButtonBusy(savePw, "Changing…");
        try {
          await call("/profile/change-password", { method: "POST", body: { current_password: cur.value, new_password: nw.value } });
          toast("Password changed. Other sessions were signed out.", "ok");
          pwForm.reset();
        } catch (e) { if (shouldSurfaceAsyncError(e)) toast(e.message, "err"); } finally { stopBusy(); }
      });
      pwForm.append(
        el("div", { class: "form-row" },
          el("div", {}, el("label", { class: "form-row__label", for: "s-cur", text: "Current password" })),
          el("div", {}, cur)),
        el("div", { class: "form-row" },
          el("div", {}, el("label", { class: "form-row__label", for: "s-new", text: "New password" }), el("p", { class: "form-row__hint", text: "At least 8 characters" })),
          el("div", {}, nw)),
        el("div", { class: "form-actions" }, savePw));

      const card = (title, sub, form) => el("section", { class: "card" },
        el("div", { class: "card__head" }, el("div", {},
          el("h2", { class: "card__title", text: title }), el("p", { class: "card__sub", text: sub }))),
        el("div", { class: "card__body" }, form));

      page.append(el("div", { class: "stack" },
        card("Profile", "How you appear to the rest of the workspace", profileForm),
        card("Preferences", "Display and notification settings", prefsForm),
        card("Password", "Changing your password signs out every other session", pwForm)));
      return page;
    });

  /* ──────────────────────── Notifications & palette ───────────────────── */

  async function badgeCount() {
    try {
      const d = await call("/notifications/unread-count");
      const unread = d ? d.unread : 0;
      $("#bell-dot").hidden = !d || d.unread === 0;
      // The dot is the only unread signal, so on its own the state is carried by
      // colour alone (1.4.1) and is invisible to a reader. The count goes into
      // the button's name instead of a second live region, which would announce
      // on every poll.
      $("#bell").setAttribute("aria-label", unread ? `Notifications, ${unread} unread` : "Notifications");
    } catch (_) { /* non-critical */ }
  }

  const notificationOwner = createGenerationOwner();

  async function openNotifications(pageNumber = q.notifications.page) {
    q.notifications.page = pageNumber;
    const ownership = notificationOwner.begin();
    const sheet = $("#notifications");
    // "Mark all read" re-enters this function with the panel already open, and it
    // may be mid-dismissal; without this the pending timeout would slide the
    // freshly reloaded panel straight back out.
    cancelExit(sheet);
    const body = clear($("#notification-body"));
    body.setAttribute("aria-busy", "true");
    body.append(el("div", { class: "stack" },
      el("span", { class: "skel skel--text skel--w80" }), el("span", { class: "skel skel--text skel--w60" })));
    if (!sheet.open) sheet.showModal();

    try {
      const result = await list(`/notifications?page=${q.notifications.page}&page_size=25`, { signal: ownership.signal });
      if (!ownership.owns()) return;
      clear(body);
      if (!result.items.length) {
        body.append(emptyState("inbox", "Nothing new", "Notifications about your workspace appear here."));
        return;
      }
      const notifications = el("ul", {});
      for (const n of result.items) {
        notifications.append(el("li", { class: `notif${n.read_at ? "" : " notif--unread"}` },
          el("p", { class: "notif__title", text: n.title }),
          n.body ? el("p", { class: "notif__text", text: n.body }) : null,
          el("time", { class: "notif__time", datetime: n.created_at, text: fmt.ago(n.created_at) })));
      }
      body.append(notifications);
      const notificationPager = pager(result.page, (next) => openNotifications(next));
      if (notificationPager) body.append(notificationPager);
    } catch (err) {
      if (!ownership.owns() || isAbort(err)) return;
      clear(body).append(inlineState("Could not load notifications", err.message, () => openNotifications(q.notifications.page)));
    } finally {
      if (ownership.owns()) body.removeAttribute("aria-busy");
    }
  }

  /* Command palette — the ⌘K surface. Navigation plus the create actions the
     caller is actually permitted to perform. */
  const palette = $("#palette");
  const paletteOwner = createGenerationOwner();
  let paletteItems = [];
  let paletteIndex = 0;

  function paletteCommands() {
    const nav = [
      { group: "Navigate", label: "Overview", glyph: "home", route: "overview" },
      { group: "Navigate", label: "Projects", glyph: "folder", route: "projects", perm: "project:view" },
      { group: "Navigate", label: "Teams", glyph: "team", route: "teams", perm: "team:view" },
      { group: "Navigate", label: "Members", glyph: "users", route: "members", perm: "member:view" },
      { group: "Navigate", label: "Roles", glyph: "shield", route: "roles", perm: "role:view" },
      { group: "Navigate", label: "Billing", glyph: "card", route: "billing", perm: "billing:view" },
      { group: "Navigate", label: "API keys", glyph: "key", route: "api-keys", perm: "apikey:view" },
      { group: "Navigate", label: "Audit log", glyph: "activity", route: "audit", perm: "audit:view" },
      { group: "Navigate", label: "Settings", glyph: "settings", route: "settings" },
    ];
    const actions = [
      { group: "Create", label: "New project", glyph: "plus", perm: "project:create", run: async ({ signal, owns }) => {
        const t = can("team:view") ? await fetchAllPages("/teams", { signal }) : { items: [] };
        if (owns()) projectForm(null, t.items);
      } },
      { group: "Create", label: "New team", glyph: "plus", perm: "team:create", run: () => teamForm(null) },
      { group: "Create", label: "Invite member", glyph: "mail", perm: "member:invite", run: async ({ signal, owns }) => {
        if (!can("role:view")) throw new Error("Role access is required to choose an invitation role.");
        const roles = await call("/roles", { signal });
        if (owns()) inviteForm(roles || []);
      } },
      { group: "Create", label: "Create API key", glyph: "key", perm: "apikey:manage", run: keyForm },
      { group: "Preferences", label: "Toggle theme", glyph: "sun", run: () => theme.cycle() },
      { group: "Preferences", label: "Sign out", glyph: "logout", run: () => signOut() },
    ];
    return nav.concat(actions).filter((c) => !c.perm || can(c.perm));
  }

  function renderPalette(query) {
    const listEl = clear($("#palette-list"));
    const q2 = query.trim().toLowerCase();
    const matches = paletteCommands().filter((c) => !q2 || c.label.toLowerCase().includes(q2));
    paletteItems = matches;
    paletteIndex = 0;

    if (!matches.length) {
      // role="status" on a node that was already in the DOM: unhiding it is the
      // change the live region reports, so "No results" is actually spoken.
      $("#palette-empty").hidden = false;
      $("#palette-query").removeAttribute("aria-activedescendant");
      return;
    }
    $("#palette-empty").hidden = true;

    let group = null;
    matches.forEach((c, i) => {
      if (c.group !== group) {
        group = c.group;
        // A listbox may only own options. role="presentation" keeps the heading
        // out of the set, so "3 of 15" stays true.
        listEl.append(el("p", { class: "palette__group", role: "presentation", text: group }));
      }
      const item = el("button", {
        // The id is what aria-activedescendant points at. It is unique within a
        // render because `i` is the match index, and the previous render's nodes
        // are removed before these are appended.
        class: "palette__item", type: "button", role: "option", id: `palette-opt-${i}`,
        // Focus belongs to the input for as long as the palette is open, so the
        // arrow-key handler bound there keeps receiving keys. Without this the
        // options are tab stops and Tab walks focus to where the arrows are dead.
        tabindex: -1,
        "aria-selected": String(i === 0), dataset: { index: String(i) },
        onclick: () => runPalette(c),
      }, icon(c.glyph), el("span", { text: c.label }), c.route ? el("span", { class: "palette__hint", text: `#/${c.route}` }) : null);
      listEl.append(item);
    });

    highlightPalette();
  }

  function highlightPalette() {
    let active = null;
    $$("#palette-list .palette__item").forEach((n, i) => {
      const on = i === paletteIndex;
      n.setAttribute("aria-selected", String(on));
      if (on) { active = n; n.scrollIntoView({ block: "nearest" }); }
    });
    // The only thing that tells a reader which row Enter will run. Without it the
    // arrow keys move a purely visual cursor and nothing is announced.
    const input = $("#palette-query");
    if (active) input.setAttribute("aria-activedescendant", active.id);
    else input.removeAttribute("aria-activedescendant");
  }

  function runPalette(cmd) {
    // Every dispatch owns a generation. Repeated Enter/click cannot let a slower
    // picker request from an older command open over a newer command's result.
    const ownership = paletteOwner.begin();
    actionOwner.invalidate();
    dismiss(palette);
    if (cmd.route) {
      window.location.hash = `#/${cmd.route}`;
      return;
    }
    if (cmd.run) {
      Promise.resolve(cmd.run({ signal: ownership.signal, owns: ownership.owns })).catch((err) => {
        if (ownership.owns() && !isAbort(err)) toast(err.message, "err");
      });
    }
  }

  // PALETTE_MODIFIER_BEGIN
  function paletteModifierLabel(platform) {
    return /Mac|iPhone|iPad|iPod/i.test(platform || "") ? "⌘" : "Ctrl";
  }
  // PALETTE_MODIFIER_END

  function openPalette() {
    paletteOwner.invalidate();
    cancelExit(palette);
    const input = $("#palette-query");
    input.value = "";
    renderPalette("");
    if (!palette.open) palette.showModal();
    input.focus();
  }

  /* ─────────────────────────────── Routing ────────────────────────────── */

  const TITLES = {
    overview: "Overview", projects: "Projects", teams: "Teams", members: "Members",
    roles: "Roles", "api-keys": "API keys", billing: "Billing", audit: "Audit log", settings: "Settings",
  };

  function route() {
    if (!state.profile) return;
    viewOwner.invalidate();
    actionOwner.invalidate();
    paletteOwner.invalidate();
    if (modal.open) closeModal();

    const raw = (window.location.hash || "#/overview").replace(/^#\/?/, "").split("?")[0];
    const name = pages[raw] ? raw : "overview";

    // A caller who deep-links to a section they lack permission for is sent to
    // the overview rather than shown a 403 they cannot act on.
    const link = $(`#nav .nav__item[data-route="${name}"]`);
    if (link && link.dataset.perm && !can(link.dataset.perm)) {
      toast("You do not have access to that section", "err");
      window.location.hash = "#/overview";
      return;
    }

    state.route = name;
    $$("#nav .nav__item, .sidebar__foot .nav__item").forEach((n) => {
      if (n.dataset.route === name) n.setAttribute("aria-current", "page");
      else n.removeAttribute("aria-current");
    });
    const label = TITLES[name] || "Overview";
    $("#crumb-page").textContent = label;
    // A hash change is not a document load, so nothing updates the window title
    // or tells a reader the view changed. Both are set here: the title is what
    // history, tab strips, and bookmarks read, the live region is what a screen
    // reader hears.
    document.title = `${label} · Tenancy`;
    $("#route-status").textContent = label;

    closeNav({ restoreFocus: false });
    host().scrollTop = 0;
    pages[name]();
    // After pages[name](), not before: view() renders its skeleton synchronously
    // before its first await, so #page has content to be read by the time focus
    // lands on it. #page carries tabindex="-1" and `.content:focus` suppresses
    // the ring, since this is a programmatic move rather than a keyboard one.
    // A no-op while a modal owns the top layer — the palette dispatching a
    // navigation is exactly that case, and its own focus restore is correct there.
    host().focus();
  }

  /* ────────────────────────────── Identity ───────────────────────────── */

  async function loadProfile() {
    state.profile = await call("/profile");
    state.perms = new Set(state.profile.permissions || []);
    // The server-side preference is authoritative on a fresh sign-in, but a
    // local override the user set on this device wins until they change it.
    // "Fresh" means nothing stored at all — not the resolved mode, which now
    // defaults to dark and would otherwise mask a saved "light" preference.
    if (theme.stored() === null && state.profile.preferences && state.profile.preferences.theme) {
      const t = state.profile.preferences.theme;
      if (t === "light" || t === "dark" || t === "system") theme.apply(t);
    }
  }

  function paintIdentity() {
    const p = state.profile;
    const org = p.organization;

    $("#org-name").textContent = org.name;
    $("#org-plan").textContent = `${org.plan_code || "free"} plan`;
    $("#org-initial").textContent = fmt.initials(org.name, org.slug).slice(0, 1);
    $("#crumb-org").textContent = org.slug;

    $("#me-initial").textContent = fmt.initials(p.full_name, p.email);
    $("#me-name").textContent = p.full_name || p.email;
    $("#me-mail").textContent = p.email;

    $$("#nav .nav__item").forEach((n) => {
      n.hidden = Boolean(n.dataset.perm && !can(n.dataset.perm));
    });
  }

  /* ──────────────────────────────── Auth ─────────────────────────────── */

  function showAuth(mode = "login") {
    $("#app").hidden = true;
    $("#auth").hidden = false;
    setMode(mode);
  }

  function setMode(mode) {
    const invite = mode === "invite";
    const reg = mode === "register";

    $("#form-login").hidden = mode !== "login";
    $("#form-register").hidden = !reg;
    $("#form-invite").hidden = !invite;
    $("#auth-alert").hidden = true;
    $(".auth__switch").hidden = invite;

    const copyMap = {
      login: ["Sign in", "Enter your credentials to continue.", "Don't have a workspace?", "Create one"],
      register: ["Create your workspace", "You become the owner with full administrative control.", "Already have an account?", "Sign in"],
      invite: ["You've been invited", "Set up your account to join the workspace.", "", ""],
    };
    const [title, sub, switchText, switchCta] = copyMap[mode];
    $("#auth-title").textContent = title;
    $("#auth-sub").textContent = sub;
    $("#auth-switch-text").textContent = switchText;
    $("#auth-switch").textContent = switchCta;
    $("#auth-switch").dataset.target = reg ? "login" : "register";
  }

  function authMsg(text, ok = false, form = null) {
    const box = $("#auth-alert");
    box.classList.toggle("alert--ok", ok);
    // Unhide first, then write. A role="alert" announces a change to a region
    // that is already in the accessibility tree.
    box.hidden = false;
    box.textContent = text;
    if (form && !ok) {
      for (const control of form.querySelectorAll("input")) {
        control.setAttribute("aria-invalid", "true");
        const described = (control.getAttribute("aria-describedby") || "").split(/\s+/).filter(Boolean);
        control.setAttribute("aria-describedby", Array.from(new Set(described.concat(box.id))).join(" "));
      }
    }
  }

  async function activateSession() {
    await loadProfile();
    paintIdentity();
    $("#auth").hidden = true;
    $("#app").hidden = false;
    badgeCount();

    if (!window.location.hash || window.location.hash === "#/") window.location.hash = "#/overview";
    else route();
  }

  function showSessionRetry(err) {
    showAuth("login");
    authMsg(`Your session is still saved, but the workspace could not be loaded: ${err.message}`);
    const retry = el("button", { class: "btn btn--secondary btn--sm", type: "button" }, "Retry session");
    retry.addEventListener("click", async () => {
      const stopBusy = setButtonBusy(retry, "Retrying…");
      try {
        await activateSession();
      } catch (retryErr) {
        if (isAbort(retryErr) || isAuthoritativeAuthError(retryErr)) return;
        authMsg(`Your session is still saved, but the workspace could not be loaded: ${retryErr.message}`);
        $("#auth-alert").append(retry);
      } finally {
        stopBusy();
      }
    });
    $("#auth-alert").append(retry);
  }

  async function enter(tokens) {
    sessionEpoch += 1;
    signedOut = false;
    state.access = tokens.access_token;
    state.refresh = tokens.refresh_token;
    session.save();
    await activateSession();
  }

  function signOut(silent = false) {
    if (signedOut) return;
    signedOut = true;
    sessionEpoch += 1;
    viewOwner.invalidate();
    paletteOwner.invalidate();
    notificationOwner.invalidate();
    actionOwner.invalidate();
    const token = state.refresh;
    session.clear();
    if (token) call("/auth/logout", { method: "POST", body: { refresh_token: token }, auth: false }).catch(() => {});
    showAuth("login");
    if (!silent) toast("Signed out", "ok");
  }

  /* ─────────────────────────────── Wiring ────────────────────────────── */

  function wireAuth() {
    $("#auth-switch").addEventListener("click", (e) => setMode(e.currentTarget.dataset.target || "register"));
    // The shell's toggle is unreachable before sign-in, so the auth screen owns
    // its own copy of the same control.
    $("#auth-theme").addEventListener("click", () => theme.cycle());

    $$("#auth form").forEach((form) => form.addEventListener("input", (event) => {
      const control = event.target.closest("input");
      if (!control) return;
      control.removeAttribute("aria-invalid");
      const ids = (control.getAttribute("aria-describedby") || "").split(/\s+/).filter((id) => id && id !== "auth-alert");
      if (ids.length) control.setAttribute("aria-describedby", ids.join(" "));
      else control.removeAttribute("aria-describedby");
    }));

    $$("[data-reveal]").forEach((btn) => {
      btn.addEventListener("click", () => {
        const input = document.getElementById(btn.dataset.reveal);
        const shown = input.type === "text";
        input.type = shown ? "password" : "text";
        btn.textContent = shown ? "Show" : "Hide";
      });
    });

    $("#form-login").addEventListener("submit", async (e) => {
      e.preventDefault();
      const btn = e.target.querySelector("[type=submit]");
      const stopBusy = setButtonBusy(btn, "Signing in…");
      try {
        const body = { email: $("#login-email").value.trim(), password: $("#login-password").value };
        const slug = $("#login-slug").value.trim();
        if (slug) body.tenant_slug = slug;

        const d = await call("/auth/login", { method: "POST", body, auth: false });
        // One email can exist in several workspaces; the API asks the client to
        // disambiguate rather than guessing.
        if (d.ambiguous_tenants && d.ambiguous_tenants.length) {
          authMsg(`This email belongs to multiple workspaces. Enter one of: ${d.ambiguous_tenants.map((t) => t.tenant_slug).join(", ")}`);
          return;
        }
        await enter(d.tokens);
      } catch (err) {
        if (isAbort(err)) return;
        if (state.access && !isAuthoritativeAuthError(err)) showSessionRetry(err);
        else authMsg(err.message, false, e.target);
      } finally { stopBusy(); }
    });

    $("#form-register").addEventListener("submit", async (e) => {
      e.preventDefault();
      const btn = e.target.querySelector("[type=submit]");
      const stopBusy = setButtonBusy(btn, "Creating…");
      try {
        const d = await call("/auth/register", { method: "POST", auth: false, body: {
          tenant_name: $("#reg-org").value.trim(),
          tenant_slug: $("#reg-slug").value.trim().toLowerCase(),
          full_name: $("#reg-name").value.trim(),
          email: $("#reg-email").value.trim(),
          password: $("#reg-password").value,
        } });
        await enter(d.tokens);
        toast("Workspace created — you are the owner", "ok");
      } catch (err) {
        if (isAbort(err)) return;
        if (state.access && !isAuthoritativeAuthError(err)) showSessionRetry(err);
        else authMsg(err.message, false, e.target);
      } finally { stopBusy(); }
    });

    $("#form-invite").addEventListener("submit", async (e) => {
      e.preventDefault();
      const btn = e.target.querySelector("[type=submit]");
      const stopBusy = setButtonBusy(btn, "Accepting…");
      try {
        await call("/invitations/accept", { method: "POST", auth: false, body: {
          token: state.inviteToken,
          full_name: $("#inv-name").value.trim(),
          password: $("#inv-password").value,
        } });
        // Accepting does not mint a session: the invitee signs in normally, so
        // token issuance stays in one place.
        history.replaceState(null, "", window.location.pathname);
        setMode("login");
        authMsg("Invitation accepted. Sign in with your new password.", true);
      } catch (err) { if (!isAbort(err)) authMsg(err.message, false, e.target); } finally { stopBusy(); }
    });
  }

  function wireShell() {
    const platform = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || "";
    $("#palette-modifier").textContent = paletteModifierLabel(platform);
    $("#signout").addEventListener("click", () => signOut());
    $("#theme-btn").addEventListener("click", () => theme.cycle());
    $("#omni").addEventListener("click", openPalette);
    $("#palette-mobile").addEventListener("click", openPalette);
    $("#bell").addEventListener("click", () => openNotifications());

    $("#read-all").addEventListener("click", async () => {
      try {
        await call("/notifications/read-all", { method: "POST" });
        await openNotifications();
        badgeCount();
      } catch (e) { if (shouldSurfaceAsyncError(e)) toast(e.message, "err"); }
    });

    $$("[data-close]").forEach((b) => b.addEventListener("click", (e) => dismiss(e.currentTarget.closest("dialog"))));

    // Esc closes a <dialog> natively and instantly, which would be the one
    // dismissal path that skips the exit animation. `cancel` is cancellable, so
    // the native close is suppressed and the same dismissal the close buttons use
    // runs instead — the dialog still closes, 150ms later, and Esc keeps working
    // if this ever fails to attach.
    [modal, palette, $("#notifications")].forEach((d) => {
      d.addEventListener("cancel", (e) => {
        if (exitTimers.has(d)) return; // already leaving: let Esc cut it short
        e.preventDefault();
        dismiss(d);
      });
    });

    $$('[data-nav="open"]').forEach((b) => b.addEventListener("click", openNav));
    $$('[data-nav="close"]').forEach((b) => b.addEventListener("click", () => closeNav()));

    // Palette keyboard model: ⌘K anywhere, arrows to move, Enter to run.
    const query = $("#palette-query");
    query.addEventListener("input", () => renderPalette(query.value));
    query.addEventListener("keydown", (e) => {
      if (e.key === "ArrowDown") { e.preventDefault(); paletteIndex = Math.min(paletteIndex + 1, paletteItems.length - 1); highlightPalette(); }
      else if (e.key === "ArrowUp") { e.preventDefault(); paletteIndex = Math.max(paletteIndex - 1, 0); highlightPalette(); }
      else if (e.key === "Enter") { e.preventDefault(); if (paletteItems[paletteIndex]) runPalette(paletteItems[paletteIndex]); }
    });

    window.addEventListener("keydown", (e) => {
      if (trapNavFocus(e)) return;
      const mod = e.metaKey || e.ctrlKey;
      if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        if (state.profile) openPalette();
        return;
      }
      // The drawer is not a <dialog>, so it gets no Escape for free, and its only
      // other dismissal was clicking the scrim — a pointer-only affordance. An
      // open dialog keeps Escape for itself: it is the topmost surface, and
      // dismissing two layers on one keypress is not what the reader asked for.
      if (e.key === "Escape" && document.body.classList.contains("nav-open") && !document.querySelector("dialog[open]")) {
        closeNav();
        return;
      }
      // "/" focuses search, but not while typing in a field.
      const inField = e.target.matches("input, textarea, select");
      if (e.key === "/" && !inField && state.profile) {
        const search = host().querySelector(".search .input");
        if (search) { e.preventDefault(); search.focus(); }
      }
    });

    drawerMedia.addEventListener("change", (event) => {
      if (!event.matches && document.body.classList.contains("nav-open")) closeNav();
    });
    window.addEventListener("hashchange", route);
  }

  /* ──────────────────────────────── Boot ─────────────────────────────── */

  async function loadInvitePreview() {
    const detail = clear($("#invite-detail"));
    const submit = $("#form-invite").querySelector("[type=submit]");
    submit.disabled = true;
    detail.classList.remove("callout--warn", "callout--err");
    detail.append(el("p", { class: "callout__title", text: "Checking invitation…" }));
    try {
      const p = await call(`/invitations/preview?token=${encodeURIComponent(state.inviteToken)}`, { auth: false });
      clear(detail).append(
        el("p", { class: "callout__title", text: `Join ${p.organization_name}` }),
        el("p", { class: "callout__text", text: `${p.email} · ${p.role_name} role · expires ${fmt.date(p.expires_at)}` }));
      $("#inv-password-field").hidden = !p.requires_password;
      $("#inv-password").required = Boolean(p.requires_password);
      submit.disabled = false;
    } catch (err) {
      clear(detail);
      if (err.status === 0 || err.status >= 500) {
        detail.classList.add("callout--warn");
        const retry = el("button", { class: "btn btn--secondary btn--sm", type: "button", onclick: loadInvitePreview }, "Retry");
        detail.append(
          el("p", { class: "callout__title", text: "Invitation could not be checked" }),
          el("p", { class: "callout__text", text: `${err.message} Your link has not been marked invalid.` }),
          retry);
      } else {
        detail.classList.add("callout--err");
        detail.append(
          el("p", { class: "callout__title", text: "This invitation is no longer valid" }),
          el("p", { class: "callout__text", text: "It may have expired or already been used. Ask for a new one." }));
      }
    }
  }

  async function boot() {
    wireAuth();
    wireShell();

    // An invite link arrives as ?invite=<token>. The preview confirms the token
    // and reports whether a password is needed, so an existing member is not
    // asked to invent a second one.
    const inviteToken = new URLSearchParams(window.location.search).get("invite");
    if (inviteToken) {
      state.inviteToken = inviteToken;
      showAuth("invite");
      await loadInvitePreview();
      return;
    }

    if (!session.load()) { showAuth("login"); return; }

    signedOut = false;
    try {
      await activateSession();
    } catch (err) {
      if (isAbort(err) || isAuthoritativeAuthError(err)) return;
      showSessionRetry(err);
    }
  }

  document.addEventListener("DOMContentLoaded", boot);
})();
