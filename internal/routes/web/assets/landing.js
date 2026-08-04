/* ============================================================================
   Tenancy — landing page behaviour
   ----------------------------------------------------------------------------
   Loaded synchronously in <head>, like theme.js, because the first thing it does
   has to happen before paint. Everything else waits for DOMContentLoaded.

   Two constraints from the server's own headers shape this file:

     - script-src 'self' means no inline script, so this is a separate
       same-origin file rather than a <script> block.
     - style-src 'self' means the style *attribute* is blocked. Programmatic
       CSSOM writes are not, so the spotlight and stagger below set custom
       properties through element.style.setProperty(). Anything that needs a
       parsed inline style is not an option here.

   And one from the dashboard's convention, kept deliberately: there is no
   innerHTML in this file. Every node is built through el(), which assigns text
   via textContent. The plan cards render data from the API, and an API response
   is not a place to start trusting markup.

   No dependencies, no build step. Same as the dashboard.
   ========================================================================== */

/* ─────────────────── Pre-paint: legacy deep-link forward ──────────────── */

/* The workspace used to be served at "/" and routed on the hash, so bookmarks,
 * history entries and shared links from before the split look like
 * "/#/projects". After the split "/" is the marketing page, which would ignore
 * the fragment and leave a returning user staring at a signup pitch — a dead end
 * produced by our own URL change. So hand it to /app with the fragment intact.
 *
 * location.replace, not assignment, so the landing page does not become a
 * history entry the back button bounces off.
 *
 * Only "#/..." is forwarded. A bare "#faq" is one of this page's own section
 * anchors, which is why the test is a prefix check and not "is there a hash".
 *
 * This runs at parse time, above everything else in the file, and returns from
 * the module immediately after — nothing below should execute on a page that is
 * already navigating away.
 */
(() => {
  "use strict";

  const hash = window.location.hash;
  const leaving = hash.length > 1 && hash.startsWith("#/");
  if (leaving) {
    window.location.replace("/app" + hash);
    return;
  }

  const reducedMotion = () =>
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  /* Scroll reveal is opt-in from the script side: app.css hides `.reveal` only
   * under `.has-reveal` on <html>. If this file fails to load or throws before
   * this line, the class is absent and every section is simply visible. A reveal
   * animation must never be able to hide content it cannot then show.
   *
   * Under prefers-reduced-motion the class is never added at all, so those
   * readers get static content rather than content that animates instantly. */
  if (!reducedMotion()) {
    document.documentElement.classList.add("has-reveal");
  }

  /* ──────────────────────────────── DOM ──────────────────────────────── */

  const $ = (sel) => document.querySelector(sel);
  const $$ = (sel) => Array.from(document.querySelectorAll(sel));

  /* The dashboard's el() helper, reduced to what this page needs. `text` is
   * assigned to textContent; there is no `html` key, deliberately. */
  function el(tag, props, ...children) {
    const node = document.createElement(tag);
    if (props) {
      for (const [key, value] of Object.entries(props)) {
        if (value === null || value === undefined || value === false) continue;
        if (key === "text") node.textContent = value;
        else if (key === "class") node.className = value;
        else node.setAttribute(key, value === true ? "" : value);
      }
    }
    for (const child of children) {
      if (child === null || child === undefined) continue;
      node.append(child);
    }
    return node;
  }

  function icon(id, cls = "icon icon--xs") {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", cls);
    svg.setAttribute("aria-hidden", "true");
    svg.setAttribute("focusable", "false");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", `#${id}`);
    svg.append(use);
    return svg;
  }

  const clear = (node) => {
    while (node.firstChild) node.removeChild(node.firstChild);
    return node;
  };

  /* ───────────────────────── Live API health ─────────────────────────── */

  /* The hero's status pill reports a real request, not a decorative "all
   * systems operational" badge. If the API is down it says so — a trust element
   * that can lie while the product is broken is worth less than no element.
   *
   * The number is a browser-side round trip, so it includes the network. It is
   * labelled "round trip" for that reason rather than presented as server
   * latency, which this cannot measure and should not imply. */
  async function reportHealth() {
    const pill = $("#status-pill");
    const text = $("#status-text");
    if (!pill || !text) return;

    const started = performance.now();
    try {
      const res = await fetch("/health", {
        headers: { Accept: "application/json" },
        cache: "no-store",
      });
      const ms = Math.max(1, Math.round(performance.now() - started));
      if (!res.ok) throw new Error(`status ${res.status}`);
      pill.dataset.state = "ok";
      text.textContent = `API healthy · ${ms} ms round trip`;
    } catch (_) {
      /* Network failure, offline, or a non-2xx. All the same to a reader: the
       * thing they would be signing up for is not answering. */
      pill.dataset.state = "down";
      text.textContent = "API unreachable from this browser";
    }
  }

  /* ─────────────────────────── Metric counters ───────────────────────── */

  /* Counts up once, when the row first scrolls into view, and only for elements
   * that declare a target. The final value is already in the HTML, so a reader
   * with JS off or reduced motion on sees the correct number immediately and
   * this only ever replaces it with the same number. */
  function countUp(node, target) {
    const duration = 700;
    const start = performance.now();
    const step = (now) => {
      const t = Math.min(1, (now - start) / duration);
      /* Ease-out cubic: fast first, settles on the value. A linear count reads
       * like a loading spinner. */
      const eased = 1 - Math.pow(1 - t, 3);
      node.textContent = String(Math.round(target * eased));
      if (t < 1) requestAnimationFrame(step);
      else node.textContent = String(target);
    };
    requestAnimationFrame(step);
  }

  /* ───────────────────────────── Reveals ─────────────────────────────── */

  /* Set by wireDemo. The lifecycle trace plays itself once, the first time the
   * section is scrolled into view: it is the centrepiece of the page and the
   * argument the whole section makes, so a reader who never thinks to press a
   * button should still see it happen. Cleared after firing, so scrolling back
   * does not replay it — the control is there for a second look. */
  let demoAutoplay = null;

  function wireReveals() {
    const targets = $$(".reveal");
    if (!targets.length) return;

    /* Stagger index for direct children, read by a calc() in the stylesheet.
     * Capped: past about eight the delay is longer than the animation and the
     * last card arrives after the reader has already looked at it. */
    for (const group of targets) {
      Array.from(group.children).forEach((child, i) => {
        child.style.setProperty("--i", String(Math.min(i, 8)));
      });
    }

    const fireAutoplay = (node) => {
      if (demoAutoplay && node.querySelector("#demo-run")) {
        demoAutoplay();
        demoAutoplay = null;
      }
    };

    if (!("IntersectionObserver" in window) || reducedMotion()) {
      for (const node of targets) {
        node.classList.add("is-in");
        fireAutoplay(node);
      }
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          entry.target.classList.add("is-in");
          /* Reveal once. Re-animating on every scroll past is the difference
           * between a page that feels alive and one that feels restless. */
          observer.unobserve(entry.target);
          fireAutoplay(entry.target);

          for (const metric of entry.target.querySelectorAll("[data-count]")) {
            const target = Number(metric.dataset.count);
            if (Number.isFinite(target)) countUp(metric, target);
          }
        }
      },
      /* The bottom margin is positive, which expands the root downward so an
       * element fires just *before* it scrolls into view.
       *
       * It used to be negative (-12%), to hold the animation back until the
       * element was properly on screen. That shrinks the root, and anything
       * sitting inside the excluded band when scrolling has already reached the
       * bottom of the document never intersects at all — so it stays at opacity
       * 0 permanently. On a tall viewport this hid the FAQ and the closing CTA
       * outright. A reveal must not be able to strand the content it is
       * revealing, and firing a little early is a far cheaper cost than
       * a section that never appears. */
      { rootMargin: "0px 0px 10% 0px", threshold: 0 }
    );

    for (const node of targets) observer.observe(node);
  }

  /* ──────────────────────────── Sticky nav ───────────────────────────── */

  /* A sentinel element observed at the top of the document, rather than a scroll
   * listener: the browser reports the crossing once instead of us recomputing on
   * every frame of every scroll. */
  function wireNav() {
    const nav = $("#ld-nav");
    if (!nav || !("IntersectionObserver" in window)) return;

    const sentinel = el("div", { class: "nav-sentinel", "aria-hidden": "true" });
    document.body.prepend(sentinel);

    new IntersectionObserver(
      ([entry]) => nav.classList.toggle("is-stuck", !entry.isIntersecting),
      { threshold: 1 }
    ).observe(sentinel);
  }

  /* ───────────────────────── Lifecycle demo ──────────────────────────── */

  /* Sample rows for two tenants. The point of the demo is that the same query,
   * with no tenant predicate, returns different rows depending only on the
   * credential — so the two lists must not overlap, and the ids are visibly from
   * different UUID prefixes. */
  const TENANTS = {
    acme: {
      label: "Acme Inc",
      slug: "acme",
      tenant: "3f9a1c74-2b18-4d6e-9c05-7ad2e1f4b830",
      token: "Bearer eyJhbGciOiJIUzI1NiJ9…acme",
      rows: [
        { id: "p_8c41d0", name: "Website redesign", status: "active" },
        { id: "p_1fb920", name: "Mobile app", status: "active" },
        { id: "p_7ad3e5", name: "Data platform", status: "archived" },
      ],
    },
    globex: {
      label: "Globex",
      slug: "globex",
      tenant: "b17e5d90-6c3f-4a21-8e77-1d90c5aa4e62",
      token: "Bearer eyJhbGciOiJIUzI1NiJ9…globex",
      rows: [
        { id: "p_d902a7", name: "Billing migration", status: "active" },
        { id: "p_44e18b", name: "Internal tools", status: "active" },
      ],
    },
  };

  function wireDemo() {
    const trace = $("#trace");
    const run = $("#demo-run");
    const rowsHost = $("#demo-rows");
    const badge = $("#demo-tenant-badge");
    if (!trace || !run || !rowsHost) return;

    const steps = Array.from(trace.querySelectorAll(".trace__step"));
    const slot = (name) => trace.querySelector(`[data-slot="${name}"]`);
    let current = "acme";
    /* Guards against a second click landing mid-run: without it two timer chains
     * would drive the same six rows and the trace would flicker between states. */
    let running = false;
    let timers = [];

    const cancel = () => {
      for (const t of timers) clearTimeout(t);
      timers = [];
    };

    function paintIdle(tenant) {
      cancel();
      running = false;
      for (const step of steps) step.dataset.state = "";
      slot("credential").textContent = tenant.token;
      slot("tenant").textContent = `tenant_id = ${tenant.tenant.slice(0, 8)}…`;
      slot("scope").textContent = `SET LOCAL app.tenant_id = '${tenant.tenant.slice(0, 8)}…'`;
      slot("rows").textContent = "— rows returned";
      if (badge) badge.textContent = tenant.slug;
      clear(rowsHost).append(
        el("p", {
          class: "pane__idle",
          text: `Send the request as ${tenant.label} to see the rows this tenant can reach.`,
        })
      );
    }

    function renderRows(tenant) {
      clear(rowsHost);
      tenant.rows.forEach((row, i) => {
        const node = el(
          "div",
          { class: "row" },
          el("code", { class: "row__id", text: row.id }),
          el("span", { class: "row__name", text: row.name }),
          el("span", {
            class: `badge ${row.status === "active" ? "badge--ok" : ""}`,
            text: row.status,
          })
        );
        node.style.setProperty("--i", String(i));
        rowsHost.append(node);
      });
      rowsHost.append(
        el("p", {
          class: "pane__idle",
          text: `${tenant.rows.length} of ${
            TENANTS.acme.rows.length + TENANTS.globex.rows.length
          } total project rows in the table. The others are not filtered out by the application — they are invisible to this transaction.`,
        })
      );
    }

    function play() {
      const tenant = TENANTS[current];
      cancel();
      running = true;
      run.disabled = true;
      for (const step of steps) step.dataset.state = "";
      clear(rowsHost);

      /* One interval for every step, short enough that the whole trace resolves
       * in about a second: this is a diagram that moves, not a cutscene. Under
       * reduced motion it resolves immediately — the information is the end
       * state, and the animation was only ever the explanation. */
      const gap = reducedMotion() ? 0 : 160;

      steps.forEach((step, i) => {
        timers.push(
          setTimeout(() => {
            step.dataset.state = "active";
            if (i > 0) steps[i - 1].dataset.state = "done";
            if (i === steps.length - 1) {
              slot("rows").textContent = `${tenant.rows.length} rows returned`;
              renderRows(tenant);
            }
          }, gap * i)
        );
      });

      timers.push(
        setTimeout(() => {
          steps[steps.length - 1].dataset.state = "done";
          run.disabled = false;
          running = false;
        }, gap * steps.length + 120)
      );
    }

    for (const button of $$("[data-tenant]")) {
      button.addEventListener("click", () => {
        current = button.dataset.tenant;
        for (const other of $$("[data-tenant]")) {
          other.setAttribute("aria-pressed", String(other === button));
        }
        paintIdle(TENANTS[current]);
      });
    }

    run.addEventListener("click", () => {
      if (running) return;
      play();
    });

    paintIdle(TENANTS[current]);

    /* Handed to wireReveals, which fires it the first time the section is on
     * screen. Guarded by `running` for the case where a reader clicks the button
     * in the same moment the reveal fires. */
    demoAutoplay = () => {
      if (!running) play();
    };
  }

  /* ────────────────────────────── Code tabs ──────────────────────────── */

  function wireCode() {
    const tabs = $$(".tab");
    if (!tabs.length) return;

    const select = (tab) => {
      for (const other of tabs) {
        const on = other === tab;
        other.setAttribute("aria-selected", String(on));
        const panel = document.getElementById(other.getAttribute("aria-controls"));
        if (panel) panel.hidden = !on;
      }
    };

    tabs.forEach((tab, i) => {
      tab.addEventListener("click", () => select(tab));
      /* Arrow-key navigation is what makes this a tablist rather than three
       * buttons that happen to be adjacent. */
      tab.addEventListener("keydown", (event) => {
        const delta = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
        if (!delta) return;
        event.preventDefault();
        const next = tabs[(i + delta + tabs.length) % tabs.length];
        next.focus();
        select(next);
      });
    });

    const copy = $("#code-copy");
    if (!copy) return;
    copy.addEventListener("click", async () => {
      const open = tabs.find((t) => t.getAttribute("aria-selected") === "true");
      const panel = open && document.getElementById(open.getAttribute("aria-controls"));
      const code = panel && panel.querySelector("code");
      if (!code || !navigator.clipboard) return;
      try {
        await navigator.clipboard.writeText(code.textContent);
        /* The button is the feedback: swapping its glyph to a tick for a beat is
         * enough, and does not steal focus the way a toast region would. */
        copy.setAttribute("aria-label", "Copied to clipboard");
        clear(copy).append(icon("i-check", "icon icon--sm"));
        setTimeout(() => {
          copy.setAttribute("aria-label", "Copy example to clipboard");
          clear(copy).append(icon("i-copy", "icon icon--sm"));
        }, 1400);
      } catch (_) {
        /* Clipboard permission denied. The code is selectable; say nothing. */
      }
    });
  }

  /* ─────────────────────── Pointer spotlight ─────────────────────────── */

  /* Writes the pointer position onto a card as two custom properties, which a
   * radial-gradient in the stylesheet follows. CSSOM, not a style attribute, so
   * the CSP permits it. Skipped for reduced motion and for coarse pointers,
   * where there is no hover to track and the listener is pure battery cost.
   *
   * Applied only to the plan cards. It used to run on the capability grid, which
   * is no longer made of cards — a spotlight needs a surface to fall on, and
   * putting one on a borderless ruled entry would be the decoration that section
   * was rewritten to remove. */
  function wireSpotlight() {
    if (reducedMotion()) return;
    if (!window.matchMedia("(hover: hover) and (pointer: fine)").matches) return;

    /* Delegated on the container, because the plan cards do not exist yet: they
     * are rendered from the API after this runs. A per-card listener would have
     * to be re-attached, and a listener that has to be remembered is a listener
     * that will be forgotten. */
    const host = $("#plans");
    if (!host) return;

    host.addEventListener("pointermove", (event) => {
      const card = event.target.closest(".plan");
      if (!card) return;
      const box = card.getBoundingClientRect();
      card.style.setProperty("--mx", `${event.clientX - box.left}px`);
      card.style.setProperty("--my", `${event.clientY - box.top}px`);
    });
  }

  /* ─────────────────────────── Live pricing ──────────────────────────── */

  /* Rendered from GET /api/v1/billing/plans, the same public endpoint any client
   * would call, so the limits on the page are the limits the server enforces.
   * Hard-coding them here would create a second source of truth that drifts the
   * first time someone edits the seed.
   *
   * Display order is explicit rather than sorted by price: the seeded enterprise
   * plan is 0 because it is quote-based, so sorting by price would file it next
   * to Free. */
  const PLAN_ORDER = ["free", "pro", "enterprise"];

  function planPrice(plan) {
    if (plan.price_cents > 0) {
      const whole = plan.price_cents / 100;
      /* No cents when there are none: "$29" not "$29.00". */
      const amount = Number.isInteger(whole) ? whole : whole.toFixed(2);
      return { amount: `$${amount}`, per: `per month` };
    }
    /* A zero price means two different things in the seeded catalog. Free reads
     * as "$0" rather than "Free" so it does not simply repeat the plan name
     * directly above it, and so the row scans as three prices. */
    return plan.code === "enterprise"
      ? { amount: "Custom", per: "quote-based" }
      : { amount: "$0", per: "no card required" };
  }

  function planLimits(plan) {
    const limit = (value, singular) =>
      value === null || value === undefined
        ? `Unlimited ${singular}s`
        : `${value} ${value === 1 ? singular : singular + "s"}`;

    const out = [limit(plan.max_seats, "seat"), limit(plan.max_projects, "project")];

    if (plan.max_storage_mb === null || plan.max_storage_mb === undefined) {
      out.push("Unlimited storage");
    } else if (plan.max_storage_mb >= 1024) {
      out.push(`${Math.round(plan.max_storage_mb / 1024)} GB storage`);
    } else {
      out.push(`${plan.max_storage_mb} MB storage`);
    }

    const features = plan.features || {};
    if (features.api_access) out.push("Scoped API keys");
    if (features.sso) out.push("SSO");
    return out;
  }

  function planCard(plan, featured) {
    const { amount, per } = planPrice(plan);
    const limits = el("ul", { class: "plan__limits" });
    for (const line of planLimits(plan)) {
      limits.append(el("li", {}, icon("i-check"), el("span", { text: line })));
    }

    return el(
      "div",
      { class: `plan${featured ? " plan--featured" : ""}` },
      featured ? el("span", { class: "plan__flag", text: "Most complete" }) : null,
      el("h3", { class: "plan__name", text: plan.name }),
      el(
        "div",
        { class: "plan__price" },
        el("span", { class: "plan__amount", text: amount }),
        el("span", { class: "plan__per", text: per })
      ),
      limits,
      el(
        "a",
        {
          class: `btn ${featured ? "btn--primary" : "btn--secondary"} plan__cta`,
          href: "/app",
        },
        el("span", { text: plan.code === "enterprise" ? "Request a quote" : "Start on this plan" })
      )
    );
  }

  async function renderPlans() {
    const host = $("#plans");
    if (!host) return;

    try {
      const res = await fetch("/api/v1/billing/plans", {
        headers: { Accept: "application/json" },
      });
      if (!res.ok) throw new Error(`status ${res.status}`);
      const payload = await res.json();
      const plans = (payload && payload.data) || [];
      if (!plans.length) throw new Error("no plans returned");

      const rank = (plan) => {
        const i = PLAN_ORDER.indexOf(plan.code);
        /* Unknown codes sort after the known ones rather than being dropped: a
         * plan the seed adds later should still appear. */
        return i === -1 ? PLAN_ORDER.length : i;
      };
      plans.sort((a, b) => rank(a) - rank(b));

      clear(host);
      for (const plan of plans) host.append(planCard(plan, plan.code === "pro"));
    } catch (_) {
      /* The section promised live data; if it is unavailable, say that rather
       * than falling back to numbers written into the page, which would quietly
       * break the promise the copy just made. */
      clear(host).append(
        el(
          "div",
          { class: "plan" },
          el("h3", { class: "plan__name", text: "Plans unavailable" }),
          el("p", {
            class: "plan__err",
            text: "The plan catalog could not be loaded from the API just now. It is served from GET /api/v1/billing/plans.",
          })
        )
      );
    }
  }

  /* ──────────────────────────────── Boot ─────────────────────────────── */

  function boot() {
    wireNav();
    /* Before wireReveals: its no-IntersectionObserver and reduced-motion path
     * reveals everything synchronously, and would look for demoAutoplay before
     * wireDemo had published it. */
    wireDemo();
    wireReveals();
    wireCode();
    wireSpotlight();
    reportHealth();
    renderPlans();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot, { once: true });
  } else {
    boot();
  }
})();
