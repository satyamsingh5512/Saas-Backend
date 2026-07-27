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
    setTimeout(() => node.remove(), kind === "err" ? 7000 : 4500);
  }

  /* ─────────────────────────────── API ────────────────────────────────── */

  class ApiError extends Error {
    constructor(status, code, message) {
      super(message || "Request failed");
      this.status = status;
      this.code = code;
    }
  }

  async function call(path, opts = {}, retried = false) {
    const { method = "GET", body, auth = true, raw = false } = opts;

    const headers = { Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (auth && state.access) headers.Authorization = `Bearer ${state.access}`;

    let res;
    try {
      res = await fetch(API + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    } catch (_) {
      throw new ApiError(0, "NETWORK", "Cannot reach the server.");
    }

    // Rotate once on 401, then give up. A refresh that itself fails means the
    // session is genuinely dead and retrying would spin.
    if (res.status === 401 && auth && !retried && state.refresh) {
      if (await rotate()) return call(path, opts, true);
      signOut(true);
      throw new ApiError(401, "UNAUTHORIZED", "Your session expired. Sign in again.");
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

  const list = (path) => call(path, { raw: true });

  async function rotate() {
    try {
      const d = await call("/auth/refresh", { method: "POST", body: { refresh_token: state.refresh }, auth: false }, true);
      state.access = d.access_token;
      state.refresh = d.refresh_token;
      session.save();
      return true;
    } catch (_) { return false; }
  }

  const can = (code) => state.perms.has(code);

  /* ─────────────────────────────── Theme ──────────────────────────────── */

  const theme = {
    current() {
      try { return localStorage.getItem(THEME_KEY) || "system"; } catch (_) { return "system"; }
    },
    apply(mode) {
      const root = document.documentElement;
      root.classList.remove("theme-dark", "theme-light");
      if (mode === "dark" || mode === "light") root.classList.add(`theme-${mode}`);
      try {
        if (mode === "system") localStorage.removeItem(THEME_KEY);
        else localStorage.setItem(THEME_KEY, mode);
      } catch (_) { /* storage blocked */ }
    },
    // Cycles system → light → dark so all three are reachable from one control.
    cycle() {
      const order = ["system", "light", "dark"];
      const next = order[(order.indexOf(theme.current()) + 1) % order.length];
      theme.apply(next);
      toast(`Theme: ${next}`, "info");
      return next;
    },
  };

  /* ────────────────────────────── Overlays ─────────────────────────────── */

  const modal = $("#modal");

  function closeModal() { if (modal.open) modal.close(); }

  function openModal({ title, desc, body, footer, wide = false }) {
    modal.classList.toggle("modal--wide", wide);
    clear(modal).append(
      el("div", { class: "modal__head" },
        el("div", {},
          el("h2", { class: "modal__title", id: "modal-title", text: title }),
          desc ? el("p", { class: "modal__desc", text: desc }) : null),
        el("button", { class: "icon-btn", type: "button", "aria-label": "Close", onclick: closeModal }, icon("x"))),
      body,
      footer);
    if (!modal.open) modal.showModal();
    const focusable = modal.querySelector("input, select, textarea, .btn--primary, .btn--danger");
    if (focusable) focusable.focus();
  }

  /**
   * formModal renders a titled form. Fields are descriptors; onSubmit receives
   * the collected values and may throw an ApiError to surface an inline error.
   */
  function formModal({ title, desc, submit, fields, onSubmit, danger = false, wide = false }) {
    const alert = el("div", { class: "alert", role: "alert", hidden: true });
    const reads = [];
    const body = el("div", { class: "modal__body" }, alert);

    for (const f of fields) {
      const id = `f-${f.name}`;
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
      } else if (f.type === "picker") {
        control = el("div", { class: "picker" });
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

      if (f.type === "checkbox") {
        body.append(el("div", { class: "check" }, control,
          el("div", {}, el("label", { class: "check__label", for: id, text: f.label }),
            f.hint ? el("p", { class: "check__hint", text: f.hint }) : null)));
      } else {
        body.append(el("div", { class: "field" },
          el("div", { class: "field__row" },
            el("label", { class: "field__label", for: id, text: f.label }),
            f.optional ? el("span", { class: "field__optional", text: "optional" }) : null),
          control,
          f.hint ? el("p", { class: "field__hint", text: f.hint }) : null));
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

    const go = el("button", { class: `btn ${danger ? "btn--danger" : "btn--primary"}`, type: "button" }, submit);
    go.addEventListener("click", async () => {
      alert.hidden = true;
      const values = {};
      for (const r of reads) values[r.name] = r.read();
      go.disabled = true;
      try {
        await onSubmit(values);
        closeModal();
      } catch (err) {
        alert.textContent = err.message;
        alert.hidden = false;
      } finally {
        go.disabled = false;
      }
    });

    openModal({
      title, desc, wide, body,
      footer: el("div", { class: "modal__foot" },
        el("button", { class: "btn btn--secondary", type: "button", onclick: closeModal }, "Cancel"),
        go),
    });
  }

  function confirmModal({ title, desc, confirm, onConfirm }) {
    const go = el("button", { class: "btn btn--danger", type: "button" }, confirm);
    go.addEventListener("click", async () => {
      go.disabled = true;
      try {
        await onConfirm();
        closeModal();
      } catch (err) {
        toast(err.message, "err");
        closeModal();
      }
    });
    openModal({
      title, desc,
      footer: el("div", { class: "modal__foot" },
        el("button", { class: "btn btn--secondary", type: "button", onclick: closeModal }, "Cancel"),
        go),
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

  function table(cols, rows) {
    const head = el("tr", {});
    for (const c of cols) {
      head.append(el("th", { class: c.actions ? "actions" : c.num ? "num" : null, scope: "col" }, c.actions ? "" : c.label));
    }
    return el("div", { class: "table-wrap" },
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

  function pager(meta, onPage) {
    if (!meta || meta.total_pages <= 1) return null;
    const from = (meta.page - 1) * meta.page_size + 1;
    const to = Math.min(meta.page * meta.page_size, meta.total);
    return el("div", { class: "pager" },
      el("span", { text: `${from}–${to} of ${meta.total}` }),
      el("div", { class: "pager__btns" },
        el("button", { class: "btn btn--secondary btn--sm", type: "button", disabled: meta.page <= 1, onclick: () => onPage(meta.page - 1) }, "Previous"),
        el("button", { class: "btn btn--secondary btn--sm", type: "button", disabled: meta.page >= meta.total_pages, onclick: () => onPage(meta.page + 1) }, "Next")));
  }

  function pageHead(title, desc, ...actions) {
    return el("header", { class: "page__head" },
      el("div", {}, el("h1", { class: "page__title", text: title }), desc ? el("p", { class: "page__desc", text: desc }) : null),
      actions.filter(Boolean).length ? el("div", { class: "page__actions" }, actions.filter(Boolean)) : null);
  }

  const rowBtn = (glyph, label, onclick, danger = false) =>
    el("button", { class: `icon-btn${danger ? " icon-btn--danger" : ""}`, type: "button", title: label, "aria-label": label, onclick }, icon(glyph));

  function searchBox(value, placeholder, onChange) {
    const input = el("input", { class: "input", type: "search", value, placeholder, "aria-label": placeholder });
    let t;
    input.addEventListener("input", () => {
      clearTimeout(t);
      t = setTimeout(() => onChange(input.value.trim()), 280);
    });
    return el("div", { class: "search" }, icon("search"), input);
  }

  function segment(options, active, onPick) {
    const wrap = el("div", { class: "segment", role: "group" });
    for (const o of options) {
      wrap.append(el("button", {
        class: "segment__btn", type: "button", "aria-pressed": String(o.value === active),
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

  async function view(skeleton, load, build) {
    render(el("div", { class: "page" }, skeleton));
    try {
      const data = await load();
      render(build(data));
    } catch (err) {
      render(el("div", { class: "page" }, el("div", { class: "card" },
        emptyState("alert", "Could not load this view", err.message,
          el("button", { class: "btn btn--secondary", type: "button", onclick: () => route() }, icon("refresh"), "Try again")))));
    }
  }

  const feedGlyph = (t) => (t === "project" ? "folder" : t === "team" ? "team" : t === "organization" ? "building" : "users");

  function feedList(events) {
    const wrap = el("div", { class: "feed" });
    for (const e of events) {
      const name = (e.metadata && e.metadata.name) || "";
      wrap.append(el("div", { class: "feed__item" },
        el("span", { class: "feed__icon" }, icon(feedGlyph(e.target_type))),
        el("div", { class: "feed__body" },
          el("p", { class: "feed__title", text: `${fmt.title(e.target_type)} ${e.verb}` }),
          el("p", { class: "feed__meta", text: [name, fmt.ago(e.created_at)].filter(Boolean).join(" · ") }))));
    }
    return wrap;
  }

  /* --- Overview --- */

  pages.overview = () => view(
    el("div", { class: "stack" }, kpiSkeleton(), el("div", { class: "card" }, tableSkeleton(3, 4))),
    async () => {
      // Both panels are permission-gated, so a Member sees the page without them
      // rather than an error.
      const [usage, activity] = await Promise.all([
        can("billing:view") ? call("/billing/usage").catch(() => null) : null,
        can("org:view") ? list("/activity?page_size=7").catch(() => ({ items: [] })) : { items: [] },
      ]);
      return { usage, activity: activity.items };
    },
    ({ usage, activity }) => {
      const org = state.profile.organization;
      const first = (state.profile.full_name || "there").split(" ")[0];

      const page = el("div", { class: "page" },
        pageHead(`Good to see you, ${first}`, `${org.name} · you hold the ${state.profile.roles[0] || "member"} role.`,
          el("button", { class: "btn btn--secondary", type: "button", onclick: () => route() }, icon("refresh"), "Refresh")));

      const kpis = el("div", { class: "kpis" });
      if (usage) {
        kpis.append(
          kpi({ label: "Seats", value: usage.seats, glyph: "users", meter: { used: usage.seats, max: usage.max_seats } }),
          kpi({ label: "Projects", value: usage.projects, glyph: "folder", meter: { used: usage.projects, max: usage.max_projects } }),
          kpi({ label: "Teams", value: usage.teams, glyph: "team", foot: "No plan limit" }),
          kpi({ label: "Plan", value: fmt.title(usage.plan_code), glyph: "card", foot: "Current subscription" }));
      } else {
        kpis.append(
          kpi({ label: "Workspace", value: org.slug, glyph: "building", foot: "Tenant identifier" }),
          kpi({ label: "Your role", value: fmt.title(state.profile.roles[0] || "member"), glyph: "shield", foot: "Assigned access" }),
          kpi({ label: "Plan", value: fmt.title(org.plan_code || "free"), glyph: "card", foot: "Current subscription" }),
          kpi({ label: "Member since", value: fmt.date(state.profile.created_at), glyph: "clock", foot: "Account created" }));
      }

      const feed = el("section", { class: "card" },
        el("div", { class: "card__head" },
          el("div", {}, el("h2", { class: "card__title", text: "Recent activity" }),
            el("p", { class: "card__sub", text: "What changed across the workspace" })),
          can("audit:view") ? el("a", { class: "link", href: "#/audit" }, "Audit log") : null));

      feed.append(activity.length
        ? el("div", { class: "card__body" }, feedList(activity))
        : emptyState("activity", "No activity yet", "Create a project or invite a teammate and it will appear here."));

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

      page.append(el("div", { class: "stack" }, kpis, el("div", { class: "split" }, feed, detail)));
      return page;
    });

  /* --- Projects --- */

  const q = {
    projects: { page: 1, search: "", status: "" },
    teams: { page: 1, search: "" },
    members: { page: 1 },
    audit: { page: 1, action: "" },
  };

  pages.projects = () => view(
    el("div", { class: "card" }, tableSkeleton(6)),
    async () => {
      const p = new URLSearchParams({ page: String(q.projects.page), page_size: "10" });
      if (q.projects.search) p.set("search", q.projects.search);
      if (q.projects.status) p.set("status", q.projects.status);
      const [projects, teams] = await Promise.all([
        list(`/projects?${p}`),
        can("team:view") ? list("/teams?page_size=100").catch(() => ({ items: [] })) : { items: [] },
      ]);
      return { projects, teams: teams.items };
    },
    ({ projects, teams }) => {
      const page = el("div", { class: "page" },
        pageHead("Projects", "Group work into projects and control who can access each one.",
          can("project:create") ? el("button", { class: "btn btn--primary", type: "button", onclick: () => projectForm(null, teams) }, icon("plus"), "New project") : null));

      const card = el("section", { class: "card" },
        el("div", { class: "toolbar" },
          searchBox(q.projects.search, "Search projects…", (v) => { q.projects.search = v; q.projects.page = 1; pages.projects(); }),
          segment([
            { value: "", label: "All" }, { value: "active", label: "Active" }, { value: "archived", label: "Archived" },
          ], q.projects.status, (v) => { q.projects.status = v; q.projects.page = 1; pages.projects(); })));

      if (!projects.items.length) {
        card.append(emptyState("folder", q.projects.search || q.projects.status ? "No matching projects" : "No projects yet",
          q.projects.search || q.projects.status ? "Try clearing the filters." : "Projects are where work lives. Create the first one to get started.",
          can("project:create") && !q.projects.search && !q.projects.status
            ? el("button", { class: "btn btn--primary", type: "button", onclick: () => projectForm(null, teams) }, icon("plus"), "New project") : null));
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
                pages.projects();
              } catch (e) { toast(e.message, "err"); }
            }));
          }
          if (can("project:delete")) {
            actions.append(rowBtn("trash", "Delete", () => confirmModal({
              title: "Delete project?",
              desc: `“${p.name}” will be removed from the workspace. This cannot be undone from the dashboard.`,
              confirm: "Delete project",
              onConfirm: async () => {
                await call(`/projects/${p.id}`, { method: "DELETE" });
                toast("Project deleted", "ok");
                pages.projects();
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
        ], rows));
        const p = pager(projects.page, (n) => { q.projects.page = n; pages.projects(); });
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
        pages.projects();
      },
    });
  }

  /* --- Teams --- */

  pages.teams = () => view(
    el("div", { class: "card" }, tableSkeleton(5)),
    () => {
      const p = new URLSearchParams({ page: String(q.teams.page), page_size: "10" });
      if (q.teams.search) p.set("search", q.teams.search);
      return list(`/teams?${p}`);
    },
    (teams) => {
      const page = el("div", { class: "page" },
        pageHead("Teams", "Teams group people. Access itself comes from roles, not team membership.",
          can("team:create") ? el("button", { class: "btn btn--primary", type: "button", onclick: () => teamForm(null) }, icon("plus"), "New team") : null));

      const card = el("section", { class: "card" },
        el("div", { class: "toolbar" },
          searchBox(q.teams.search, "Search teams…", (v) => { q.teams.search = v; q.teams.page = 1; pages.teams(); })));

      if (!teams.items.length) {
        card.append(emptyState("team", q.teams.search ? "No matching teams" : "No teams yet",
          q.teams.search ? "Try a different search." : "Create a team to group people around a shared area of work.",
          can("team:create") && !q.teams.search
            ? el("button", { class: "btn btn--primary", type: "button", onclick: () => teamForm(null) }, icon("plus"), "New team") : null));
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
                pages.teams();
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
        ], rows));
        const p = pager(teams.page, (n) => { q.teams.page = n; pages.teams(); });
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
        pages.teams();
      },
    });
  }

  /** membersSheet manages the roster of a team or project in a slide-over. */
  async function membersSheet(kind, resource) {
    const base = kind === "team" ? "teams" : "projects";
    const perm = kind === "team" ? "team:manage" : "project:manage";

    let members = [];
    let people = [];
    try {
      const [m, u] = await Promise.all([
        list(`/${base}/${resource.id}/members?page_size=100`),
        can("member:view") ? list("/users?page_size=100").catch(() => ({ items: [] })) : { items: [] },
      ]);
      members = m.items;
      people = u.items;
    } catch (err) {
      toast(err.message, "err");
      return;
    }

    const have = new Set(members.map((m) => m.user_id));
    const available = people.filter((p) => !have.has(p.id));

    const roster = el("div", { class: "stack" });
    if (!members.length) {
      roster.append(emptyState("users", "No members yet", `Nobody has been added to ${resource.name}.`));
    } else {
      for (const m of members) {
        roster.append(el("div", { class: "feed__item" },
          el("span", { class: "avatar avatar--md", text: fmt.initials(m.full_name, m.email) }),
          el("div", { class: "feed__body" },
            el("p", { class: "feed__title", text: m.full_name || m.email }),
            el("p", { class: "feed__meta", text: m.email })),
          can(perm) ? rowBtn("x", `Remove ${m.email}`, async () => {
            try {
              await call(`/${base}/${resource.id}/members/${m.user_id}`, { method: "DELETE" });
              toast("Member removed", "ok");
              closeModal();
              kind === "team" ? pages.teams() : pages.projects();
            } catch (e) { toast(e.message, "err"); }
          }, true) : null));
      }
    }

    const body = el("div", { class: "modal__body" }, roster);

    if (can(perm)) {
      if (!available.length) {
        body.append(el("p", { class: "field__hint", text: "Everyone in the organization is already a member." }));
      } else {
        const select = el("select", { class: "select", "aria-label": "Choose someone to add" });
        for (const p of available) select.append(el("option", { value: p.id }, `${p.full_name || p.email} · ${p.email}`));

        const add = el("button", { class: "btn btn--primary btn--block", type: "button" }, icon("plus"), `Add to ${kind}`);
        add.addEventListener("click", async () => {
          add.disabled = true;
          try {
            await call(`/${base}/${resource.id}/members`, { method: "POST", body: { user_id: select.value } });
            toast("Member added", "ok");
            closeModal();
            kind === "team" ? pages.teams() : pages.projects();
          } catch (e) {
            toast(e.message, "err");
            add.disabled = false;
          }
        });

        body.append(el("hr", { class: "divider" }),
          el("div", { class: "field" }, el("label", { class: "field__label", text: "Add a member" }), select), add);
      }
    }

    openModal({
      title: `${resource.name} members`,
      desc: "Membership groups people around this work. It grants no permissions by itself.",
      body,
    });
  }

  /* --- Members & invitations --- */

  pages.members = () => view(
    el("div", { class: "stack" }, el("div", { class: "card" }, tableSkeleton(5)), el("div", { class: "card" }, tableSkeleton(4, 3))),
    async () => {
      const [users, invites, roles] = await Promise.all([
        list(`/users?page=${q.members.page}&page_size=10`),
        can("member:view") ? list("/invitations?status=pending&page_size=50").catch(() => ({ items: [] })) : { items: [] },
        can("role:view") ? call("/roles").catch(() => []) : [],
      ]);
      return { users, invites: invites.items, roles: roles || [] };
    },
    ({ users, invites, roles }) => {
      const page = el("div", { class: "page" },
        pageHead("Members", "Everyone with access to this workspace, plus invitations still outstanding.",
          can("member:invite") ? el("button", { class: "btn btn--primary", type: "button", onclick: () => inviteForm(roles) }, icon("mail"), "Invite member") : null));

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
      ], rows));
      const pg = pager(users.page, (n) => { q.members.page = n; pages.members(); });
      if (pg) dir.append(pg);

      const stack = el("div", { class: "stack" }, dir);

      if (can("member:view")) {
        const inv = el("section", { class: "card" },
          el("div", { class: "card__head" }, el("div", {},
            el("h2", { class: "card__title", text: "Pending invitations" }),
            el("p", { class: "card__sub", text: "Pending invitations count against the plan's seat limit" }))));

        if (!invites.length) {
          inv.append(emptyState("mail", "No pending invitations", "Everyone invited has responded."));
        } else {
          const irows = invites.map((i) => {
            const actions = el("div", { class: "row-actions" });
            if (can("member:invite")) {
              actions.append(rowBtn("trash", "Revoke", () => confirmModal({
                title: "Revoke invitation?",
                desc: `The link sent to ${i.email} will stop working immediately.`,
                confirm: "Revoke invitation",
                onConfirm: async () => {
                  await call(`/invitations/${i.id}`, { method: "DELETE" });
                  toast("Invitation revoked", "ok");
                  pages.members();
                },
              }), true));
            }
            return el("tr", {},
              el("td", {}, el("span", { class: "mono", text: i.email })),
              el("td", {}, badge(i.role_name || "Member", "brand")),
              el("td", {}, el("span", { class: "cell--muted", text: `Expires ${fmt.date(i.expires_at)}` })),
              el("td", { class: "actions" }, actions));
          });
          inv.append(table([{ label: "Email" }, { label: "Role" }, { label: "Expiry" }, { actions: true }], irows));
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
        { name: "role_id", label: "Role", type: "select", options: roles.map((r) => ({ value: r.id, label: r.name })) },
      ],
      onSubmit: async (v) => {
        const res = await call("/invitations", { method: "POST", body: { email: v.email, role_id: v.role_id } });
        pages.members();
        setTimeout(() => showInvite(v.email, res.token), 140);
      },
    });
  }

  function showInvite(email, token) {
    const link = `${window.location.origin}/?invite=${encodeURIComponent(token)}`;
    openModal({
      title: "Invitation created",
      desc: `Send this single-use link to ${email}. It is shown once and cannot be retrieved again.`,
      body: el("div", { class: "modal__body" },
        el("div", { class: "secret" },
          el("code", { text: link }),
          el("button", { class: "icon-btn", type: "button", title: "Copy link", "aria-label": "Copy invitation link", onclick: () => copy(link) }, icon("copy"))),
        el("p", { class: "field__hint", text: "No email transport is configured in this deployment, so the link is surfaced here instead of being sent." })),
      footer: el("div", { class: "modal__foot" }, el("button", { class: "btn btn--primary", type: "button", onclick: closeModal }, "Done")),
    });
  }

  /* --- Roles --- */

  pages.roles = () => view(
    el("div", { class: "card" }, tableSkeleton(5)),
    async () => {
      const [roles, catalog] = await Promise.all([call("/roles"), call("/permissions")]);
      return { roles: roles || [], catalog: catalog || [] };
    },
    ({ roles, catalog }) => {
      const page = el("div", { class: "page" },
        pageHead("Roles", "Permissions resolve from the database on every request, so edits take effect immediately.",
          can("role:manage") ? el("button", { class: "btn btn--primary", type: "button", onclick: () => roleForm(catalog) }, icon("plus"), "New role") : null));

      const rows = roles.map((r) => {
        const actions = el("div", { class: "row-actions" });
        if (can("role:manage")) {
          actions.append(rowBtn("edit", "Edit permissions", () => rolePerms(r, catalog)));
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
                pages.roles();
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
      ], rows));

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
        pages.roles();
      },
    });
  }

  function rolePerms(role, catalog) {
    formModal({
      title: `${role.name} permissions`,
      desc: role.is_system
        ? "This is a system role: its permission set is editable, but it cannot be renamed or deleted."
        : "Choose exactly which capabilities this role grants.",
      submit: "Save permissions",
      wide: true,
      fields: [{ name: "permission_codes", label: "Permissions", type: "picker", options: catalog, value: [] }],
      onSubmit: async (v) => {
        await call(`/roles/${role.id}/permissions`, { method: "PUT", body: { permission_codes: v.permission_codes } });
        toast("Permissions updated", "ok");
        pages.roles();
      },
    });
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

  pages["api-keys"] = () => view(
    el("div", { class: "card" }, tableSkeleton(6)),
    () => list("/api-keys?page_size=50"),
    (keys) => {
      const page = el("div", { class: "page" },
        pageHead("API keys", "Machine credentials for server-to-server access, limited to the scopes you choose.",
          can("apikey:manage") ? el("button", { class: "btn btn--primary", type: "button", onclick: keyForm }, icon("plus"), "Create key") : null));

      const card = el("section", { class: "card" });

      if (!keys.items.length) {
        card.append(emptyState("key", "No API keys",
          "Create a key so a CI pipeline or service can call the API without a user session.",
          can("apikey:manage") ? el("button", { class: "btn btn--primary", type: "button", onclick: keyForm }, icon("plus"), "Create key") : null));
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
                pages["api-keys"]();
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
        ], rows));
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
      onSubmit: async (v) => {
        if (!v.scopes.length) throw new ApiError(400, "VALIDATION", "Choose at least one scope.");
        const body = { name: v.name, scopes: v.scopes };
        if (v.expires_at) body.expires_at = new Date(`${v.expires_at}T23:59:59Z`).toISOString();
        const res = await call("/api-keys", { method: "POST", body });
        pages["api-keys"]();
        setTimeout(() => showSecret(res.secret), 140);
      },
    });
  }

  function showSecret(secret) {
    openModal({
      title: "Copy your API key",
      desc: "This is the only time the key is shown. Store it in your secret manager now.",
      body: el("div", { class: "modal__body" },
        el("div", { class: "secret" },
          el("code", { text: secret }),
          el("button", { class: "icon-btn", type: "button", title: "Copy key", "aria-label": "Copy API key", onclick: () => copy(secret) }, icon("copy"))),
        el("p", { class: "field__hint", text: "Send it as an X-API-Key header, or as Authorization: Bearer. If it leaks, revoke it here immediately." })),
      footer: el("div", { class: "modal__foot" }, el("button", { class: "btn btn--primary", type: "button", onclick: closeModal }, "I've stored it")),
    });
  }

  /* --- Billing --- */

  pages.billing = () => view(
    el("div", { class: "stack" }, kpiSkeleton(), el("div", { class: "card" }, tableSkeleton(3, 3))),
    async () => {
      const [plans, sub, usage] = await Promise.all([
        call("/billing/plans", { auth: false }), call("/billing/subscription"), call("/billing/usage"),
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
            pages.billing();
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
        pages.billing();
      },
    });
  }

  /* --- Audit --- */

  pages.audit = () => view(
    el("div", { class: "card" }, tableSkeleton(4, 8)),
    async () => {
      const p = new URLSearchParams({ page: String(q.audit.page), page_size: "15" });
      if (q.audit.action) p.set("action", q.audit.action);
      const [logs, activity] = await Promise.all([
        list(`/audit-logs?${p}`),
        can("org:view") ? list("/activity?page_size=8").catch(() => ({ items: [] })) : { items: [] },
      ]);
      return { logs, activity: activity.items };
    },
    ({ logs, activity }) => {
      const page = el("div", { class: "page" },
        pageHead("Audit log", "An append-only record of security-relevant actions. The application role holds no UPDATE or DELETE grant on this table."));

      const card = el("section", { class: "card" },
        el("div", { class: "toolbar" },
          searchBox(q.audit.action, "Filter by action, e.g. team.created", (v) => { q.audit.action = v; q.audit.page = 1; pages.audit(); })));

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
        card.append(table([{ label: "Action" }, { label: "Target" }, { label: "Source IP" }, { label: "When" }], rows));
        const pg = pager(logs.page, (n) => { q.audit.page = n; pages.audit(); });
        if (pg) card.append(pg);
      }

      const feed = el("section", { class: "card" },
        el("div", { class: "card__head" }, el("div", {},
          el("h2", { class: "card__title", text: "Activity" }),
          el("p", { class: "card__sub", text: "The product-facing counterpart to the audit trail" }))));
      feed.append(activity.length
        ? el("div", { class: "card__body" }, feedList(activity))
        : emptyState("activity", "Nothing yet", "Activity appears as people work in the workspace."));

      page.append(el("div", { class: "split" }, card, feed));
      return page;
    });

  /* --- Settings --- */

  pages.settings = () => view(
    el("div", { class: "card" }, tableSkeleton(2, 5)),
    async () => {
      const [profile, prefs] = await Promise.all([call("/profile"), call("/preferences")]);
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
        saveProfile.disabled = true;
        try {
          const body = { full_name: name.value.trim() };
          if (avatar.value.trim()) body.avatar_url = avatar.value.trim();
          else body.clear_avatar = true;
          await call("/profile", { method: "PATCH", body });
          toast("Profile updated", "ok");
          await loadProfile();
          paintIdentity();
        } catch (e) { toast(e.message, "err"); } finally { saveProfile.disabled = false; }
      });
      profileForm.append(
        el("div", { class: "form-row" },
          el("div", {}, el("p", { class: "form-row__label", text: "Full name" }), el("p", { class: "form-row__hint", text: "Shown to teammates across the workspace" })),
          el("div", {}, name)),
        el("div", { class: "form-row" },
          el("div", {}, el("p", { class: "form-row__label", text: "Avatar URL" }), el("p", { class: "form-row__hint", text: "Must be an absolute https URL" })),
          el("div", {}, avatar)),
        el("div", { class: "form-row" },
          el("div", {}, el("p", { class: "form-row__label", text: "Email" }), el("p", { class: "form-row__hint", text: "Contact an administrator to change this" })),
          el("div", {}, el("input", { class: "input", type: "email", value: profile.email, disabled: true }))),
        el("div", { class: "form-actions" }, saveProfile));

      /* Preferences */
      const themeSel = el("select", { class: "select", id: "s-theme" });
      for (const t of ["system", "light", "dark"]) {
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
        savePrefs.disabled = true;
        try {
          await call("/preferences", {
            method: "PATCH",
            body: { theme: themeSel.value, timezone: tz.value.trim(), locale: locale.value.trim(), email_notifications: mails.checked },
          });
          theme.apply(themeSel.value);
          toast("Preferences saved", "ok");
        } catch (e) { toast(e.message, "err"); } finally { savePrefs.disabled = false; }
      });
      prefsForm.append(
        el("div", { class: "form-row" },
          el("div", {}, el("p", { class: "form-row__label", text: "Theme" }), el("p", { class: "form-row__hint", text: "System follows your operating system" })),
          el("div", {}, themeSel)),
        el("div", { class: "form-row" },
          el("div", {}, el("p", { class: "form-row__label", text: "Timezone" })),
          el("div", {}, tz)),
        el("div", { class: "form-row" },
          el("div", {}, el("p", { class: "form-row__label", text: "Locale" })),
          el("div", {}, locale)),
        el("div", { class: "form-row" },
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
        savePw.disabled = true;
        try {
          await call("/profile/change-password", { method: "POST", body: { current_password: cur.value, new_password: nw.value } });
          toast("Password changed. Other sessions were signed out.", "ok");
          pwForm.reset();
        } catch (e) { toast(e.message, "err"); } finally { savePw.disabled = false; }
      });
      pwForm.append(
        el("div", { class: "form-row" },
          el("div", {}, el("p", { class: "form-row__label", text: "Current password" })),
          el("div", {}, cur)),
        el("div", { class: "form-row" },
          el("div", {}, el("p", { class: "form-row__label", text: "New password" }), el("p", { class: "form-row__hint", text: "At least 8 characters" })),
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
      $("#bell-dot").hidden = !d || d.unread === 0;
    } catch (_) { /* non-critical */ }
  }

  async function openNotifications() {
    const sheet = $("#notifications");
    const body = clear($("#notification-body"));
    body.append(el("div", { class: "stack" },
      el("span", { class: "skel skel--text skel--w80" }), el("span", { class: "skel skel--text skel--w60" })));
    if (!sheet.open) sheet.showModal();

    try {
      const { items } = await list("/notifications?page_size=25");
      clear(body);
      if (!items.length) {
        body.append(emptyState("inbox", "Nothing new", "Notifications about your workspace appear here."));
        return;
      }
      for (const n of items) {
        body.append(el("div", { class: `notif${n.read_at ? "" : " notif--unread"}` },
          el("p", { class: "notif__title", text: n.title }),
          n.body ? el("p", { class: "notif__text", text: n.body }) : null,
          el("time", { class: "notif__time", datetime: n.created_at, text: fmt.ago(n.created_at) })));
      }
    } catch (err) {
      clear(body).append(emptyState("alert", "Could not load notifications", err.message));
    }
  }

  /* Command palette — the ⌘K surface. Navigation plus the create actions the
     caller is actually permitted to perform. */
  const palette = $("#palette");
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
      { group: "Create", label: "New project", glyph: "plus", perm: "project:create", run: async () => {
        const t = can("team:view") ? await list("/teams?page_size=100").catch(() => ({ items: [] })) : { items: [] };
        projectForm(null, t.items);
      } },
      { group: "Create", label: "New team", glyph: "plus", perm: "team:create", run: () => teamForm(null) },
      { group: "Create", label: "Invite member", glyph: "mail", perm: "member:invite", run: async () => {
        const roles = can("role:view") ? await call("/roles").catch(() => []) : [];
        inviteForm(roles || []);
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
      listEl.append(el("p", { class: "palette__empty", text: "No results" }));
      return;
    }

    let group = null;
    matches.forEach((c, i) => {
      if (c.group !== group) {
        group = c.group;
        listEl.append(el("p", { class: "palette__group", text: group }));
      }
      const item = el("button", {
        class: "palette__item", type: "button", role: "option",
        "aria-selected": String(i === 0), dataset: { index: String(i) },
        onclick: () => runPalette(c),
      }, icon(c.glyph), el("span", { text: c.label }), c.route ? el("span", { class: "palette__hint", text: `#/${c.route}` }) : null);
      listEl.append(item);
    });
  }

  function highlightPalette() {
    $$("#palette-list .palette__item").forEach((n, i) => {
      const on = i === paletteIndex;
      n.setAttribute("aria-selected", String(on));
      if (on) n.scrollIntoView({ block: "nearest" });
    });
  }

  function runPalette(cmd) {
    palette.close();
    if (cmd.route) window.location.hash = `#/${cmd.route}`;
    else if (cmd.run) cmd.run();
  }

  function openPalette() {
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
    $("#crumb-page").textContent = TITLES[name] || "Overview";

    document.body.classList.remove("nav-open");
    $(".scrim").hidden = true;
    host().scrollTop = 0;
    pages[name]();
  }

  /* ────────────────────────────── Identity ───────────────────────────── */

  async function loadProfile() {
    state.profile = await call("/profile");
    state.perms = new Set(state.profile.permissions || []);
    // The server-side preference is authoritative on a fresh sign-in, but a
    // local override the user set on this device wins until they change it.
    const stored = theme.current();
    if (stored === "system" && state.profile.preferences && state.profile.preferences.theme) {
      const t = state.profile.preferences.theme;
      if (t === "light" || t === "dark") theme.apply(t);
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

  function authMsg(text, ok = false) {
    const box = $("#auth-alert");
    box.textContent = text;
    box.classList.toggle("alert--ok", ok);
    box.hidden = false;
  }

  async function enter(tokens) {
    state.access = tokens.access_token;
    state.refresh = tokens.refresh_token;
    session.save();

    await loadProfile();
    paintIdentity();
    $("#auth").hidden = true;
    $("#app").hidden = false;
    badgeCount();

    if (!window.location.hash || window.location.hash === "#/") window.location.hash = "#/overview";
    else route();
  }

  function signOut(silent = false) {
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
      btn.disabled = true;
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
      } catch (err) { authMsg(err.message); } finally { btn.disabled = false; }
    });

    $("#form-register").addEventListener("submit", async (e) => {
      e.preventDefault();
      const btn = e.target.querySelector("[type=submit]");
      btn.disabled = true;
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
      } catch (err) { authMsg(err.message); } finally { btn.disabled = false; }
    });

    $("#form-invite").addEventListener("submit", async (e) => {
      e.preventDefault();
      const btn = e.target.querySelector("[type=submit]");
      btn.disabled = true;
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
      } catch (err) { authMsg(err.message); } finally { btn.disabled = false; }
    });
  }

  function wireShell() {
    $("#signout").addEventListener("click", () => signOut());
    $("#theme-btn").addEventListener("click", () => theme.cycle());
    $("#omni").addEventListener("click", openPalette);
    $("#bell").addEventListener("click", openNotifications);

    $("#read-all").addEventListener("click", async () => {
      try {
        await call("/notifications/read-all", { method: "POST" });
        await openNotifications();
        badgeCount();
      } catch (e) { toast(e.message, "err"); }
    });

    $$("[data-close]").forEach((b) => b.addEventListener("click", (e) => e.currentTarget.closest("dialog").close()));

    const openNav = () => { document.body.classList.add("nav-open"); $(".scrim").hidden = false; };
    const closeNav = () => { document.body.classList.remove("nav-open"); $(".scrim").hidden = true; };
    $$('[data-nav="open"]').forEach((b) => b.addEventListener("click", openNav));
    $$('[data-nav="close"]').forEach((b) => b.addEventListener("click", closeNav));

    // Palette keyboard model: ⌘K anywhere, arrows to move, Enter to run.
    const query = $("#palette-query");
    query.addEventListener("input", () => renderPalette(query.value));
    query.addEventListener("keydown", (e) => {
      if (e.key === "ArrowDown") { e.preventDefault(); paletteIndex = Math.min(paletteIndex + 1, paletteItems.length - 1); highlightPalette(); }
      else if (e.key === "ArrowUp") { e.preventDefault(); paletteIndex = Math.max(paletteIndex - 1, 0); highlightPalette(); }
      else if (e.key === "Enter") { e.preventDefault(); if (paletteItems[paletteIndex]) runPalette(paletteItems[paletteIndex]); }
    });

    window.addEventListener("keydown", (e) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        if (state.profile) openPalette();
        return;
      }
      // "/" focuses search, but not while typing in a field.
      const inField = e.target.matches("input, textarea, select");
      if (e.key === "/" && !inField && state.profile) {
        const search = host().querySelector(".search .input");
        if (search) { e.preventDefault(); search.focus(); }
      }
    });

    window.addEventListener("hashchange", route);
  }

  /* ──────────────────────────────── Boot ─────────────────────────────── */

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
      try {
        const p = await call(`/invitations/preview?token=${encodeURIComponent(inviteToken)}`, { auth: false });
        clear($("#invite-detail")).append(
          el("p", { class: "callout__title", text: `Join ${p.organization_name}` }),
          el("p", { class: "callout__text", text: `${p.email} · ${p.role_name} role · expires ${fmt.date(p.expires_at)}` }));
        $("#inv-password-field").hidden = !p.requires_password;
        if (p.requires_password) $("#inv-password").required = true;
      } catch (_) {
        clear($("#invite-detail")).append(
          el("p", { class: "callout__title", text: "This invitation is no longer valid" }),
          el("p", { class: "callout__text", text: "It may have expired or already been used. Ask for a new one." }));
        $("#form-invite").querySelector("[type=submit]").disabled = true;
      }
      return;
    }

    if (!session.load()) { showAuth("login"); return; }

    try {
      await loadProfile();
      paintIdentity();
      $("#auth").hidden = true;
      $("#app").hidden = false;
      badgeCount();
      route();
    } catch (_) {
      session.clear();
      showAuth("login");
    }
  }

  document.addEventListener("DOMContentLoaded", boot);
})();
