const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const appPath = path.join(__dirname, "..", "web", "assets", "app.js");
const htmlPath = path.join(__dirname, "..", "web", "index.html");
const cssPath = path.join(__dirname, "..", "web", "assets", "app.css");
const appSource = fs.readFileSync(appPath, "utf8");
const htmlSource = fs.readFileSync(htmlPath, "utf8");
const cssSource = fs.readFileSync(cssPath, "utf8");

function extract(beginName, endName, exportName, context = {}) {
  const beginMarker = `// ${beginName}`;
  const endMarker = `// ${endName}`;
  const begin = appSource.indexOf(beginMarker);
  const end = appSource.indexOf(endMarker);
  assert.notEqual(begin, -1, `${beginName} marker is missing`);
  assert.notEqual(end, -1, `${endName} marker is missing`);
  assert.ok(end > begin, `${beginName} markers are out of order`);
  vm.runInNewContext(
    `${appSource.slice(begin + beginMarker.length, end)}\nthis.exported = ${exportName};`,
    context,
  );
  return context.exported;
}

function extractRange(beginText, endText, exportName, context = {}) {
  const begin = appSource.indexOf(beginText);
  const end = appSource.indexOf(endText, begin);
  assert.notEqual(begin, -1, `${beginText} is missing`);
  assert.notEqual(end, -1, `${endText} is missing`);
  assert.ok(end > begin, `${beginText} and ${endText} are out of order`);
  vm.runInNewContext(`${appSource.slice(begin, end)}\nthis.exported = ${exportName};`, context);
  return context.exported;
}

const createSingleflight = extract(
  "REFRESH_SINGLEFLIGHT_BEGIN",
  "REFRESH_SINGLEFLIGHT_END",
  "createSingleflight",
);
const createGenerationOwner = extract(
  "VIEW_OWNERSHIP_BEGIN",
  "VIEW_OWNERSHIP_END",
  "createGenerationOwner",
  { AbortController },
);
const honestLoad = extract("HONEST_LOAD_BEGIN", "HONEST_LOAD_END", "honestLoad");
const fetchAllPages = extract("FETCH_ALL_PAGES_BEGIN", "FETCH_ALL_PAGES_END", "fetchAllPages");
const shouldSurfaceAsyncError = extract(
  "ASYNC_ERROR_VISIBILITY_BEGIN",
  "ASYNC_ERROR_VISIBILITY_END",
  "shouldSurfaceAsyncError",
  { isAbort: (err) => Boolean(err && err.name === "AbortError") },
);

class FakeElement {
  constructor(tag, props = {}) {
    this.tag = tag;
    this.children = [];
    this.listeners = new Map();
    this.attributes = new Map();
    this.disabled = false;
    this.hidden = Boolean(props.hidden);
    this.id = props.id || "";
    this.textContent = props.text || "";
    this.value = props.value || "";
    this.open = false;
    this.inert = false;
    this.focused = false;
    const classes = new Set();
    this.classList = {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      contains: (name) => classes.has(name),
      toggle: (name, force) => {
        const on = force === undefined ? !classes.has(name) : Boolean(force);
        if (on) classes.add(name); else classes.delete(name);
        return on;
      },
    };
    if (props.class) String(props.class).split(/\s+/).filter(Boolean).forEach((name) => classes.add(name));
    if (tag === "form") {
      this.checkValidity = () => true;
      this.reportValidity = () => {};
    }
  }

  get childNodes() { return this.children; }
  get firstChild() { return this.children[0] || null; }

  append(...children) {
    for (const child of children.flat()) {
      // Native Element.append stringifies non-Nodes, including undefined. Tests
      // therefore catch optional regions that were not filtered by the caller.
      const appended = child instanceof FakeElement ? child : String(child);
      if (appended instanceof FakeElement) appended.parentElement = this;
      this.children.push(appended);
    }
  }

  removeChild(child) {
    const index = this.children.indexOf(child);
    if (index >= 0) this.children.splice(index, 1);
    return child;
  }

  addEventListener(type, listener) { this.listeners.set(type, listener); }
  dispatch(type, event = {}) { return this.listeners.get(type)(event); }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.has(name) ? this.attributes.get(name) : null; }
  removeAttribute(name) { this.attributes.delete(name); }
  focus() { this.focused = true; }
  showModal() { this.open = true; }

  contains(target) {
    if (target === this) return true;
    return this.children.some((child) => child instanceof FakeElement && child.contains(target));
  }

  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }

  querySelectorAll(selector) {
    const matches = [];
    const tags = new Set(selector.split(",").map((item) => item.trim()));
    const visit = (node) => {
      if (!(node instanceof FakeElement)) return;
      if (tags.has(node.tag)) matches.push(node);
      node.children.forEach(visit);
    };
    this.children.forEach(visit);
    return matches;
  }
}

const clearFake = (node) => {
  while (node.firstChild) node.removeChild(node.firstChild);
  return node;
};
const fakeIcon = (name, cls) => new FakeElement("svg", { class: cls, id: name });
const busyHelpers = extract(
  "BUTTON_BUSY_BEGIN",
  "BUTTON_BUSY_END",
  "({ setButtonBusy, pendingButtonLabel })",
  { clear: clearFake, icon: fakeIcon },
);
const setButtonBusy = busyHelpers.setButtonBusy;
const pendingButtonLabel = busyHelpers.pendingButtonLabel;
const collectionCreateActions = extract(
  "COLLECTION_CREATE_ACTIONS_BEGIN",
  "COLLECTION_CREATE_ACTIONS_END",
  "collectionCreateActions",
);
const paletteModifierLabel = extract(
  "PALETTE_MODIFIER_BEGIN",
  "PALETTE_MODIFIER_END",
  "paletteModifierLabel",
);

function modalHarness() {
  let opened = null;
  let closeCalls = 0;
  const toasts = [];
  const context = {
    actionOwner: { begin() { throw new Error("test must provide explicit ownership"); } },
    el(tag, props, ...children) {
      const node = new FakeElement(tag, props || {});
      node.append(...children.flat().filter((child) => child !== null && child !== undefined && child !== false));
      return node;
    },
    setButtonBusy,
    pendingButtonLabel,
    openModal(options) { opened = options; return true; },
    closeModal() { closeCalls += 1; },
    shouldSurfaceAsyncError,
    toast(message, kind) { toasts.push({ message, kind }); },
  };
  const functions = extractRange(
    "function formModal(",
    "/* ───────────────────────────── Components",
    "({ formModal, confirmModal })",
    context,
  );
  return {
    ...functions,
    opened: () => opened,
    closeCalls: () => closeCalls,
    toasts,
  };
}

class TestApiError extends Error {
  constructor(status, code, message) {
    super(message || "Request failed");
    this.status = status;
    this.code = code;
  }
}

function response(status, data = null, error = null) {
  return {
    status,
    ok: status >= 200 && status < 300,
    json: async () => error ? { error } : { data },
  };
}

function authHarness(fetchImpl) {
  const state = { access: "old-a", refresh: "old-r" };
  let signOutCalls = 0;
  let signedOut = false;
  const context = {
    API: "/api/v1",
    ApiError: TestApiError,
    createSingleflight,
    fetch: fetchImpl,
    isAbort: (err) => Boolean(err && err.name === "AbortError"),
    isAuthoritativeAuthError: (err) => Boolean(err && err.status === 401),
    session: { save() {} },
    sessionEpoch: 1,
    state,
    signOut() {
      if (signedOut) return;
      signedOut = true;
      signOutCalls += 1;
      context.sessionEpoch += 1;
      state.access = null;
      state.refresh = null;
    },
  };
  const api = extract("AUTH_REQUEST_BEGIN", "AUTH_REQUEST_END", "({ call, rotate })", context);
  return { api, context, state, signOutCalls: () => signOutCalls };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

test("busy helper disables, exposes progress, and restores exact button children", () => {
  const button = new FakeElement("button");
  const glyph = new FakeElement("svg", { id: "plus" });
  const label = new FakeElement("span", { text: "Create project" });
  button.append(glyph, label);

  const restore = setButtonBusy(button, "Creating…");
  assert.equal(button.disabled, true);
  assert.equal(button.getAttribute("aria-busy"), "true");
  assert.equal(button.children[0].tag, "svg");
  assert.equal(button.children[0].id, "refresh");
  assert.equal(button.children[1], "Creating…");

  restore();
  assert.equal(button.disabled, false);
  assert.equal(button.getAttribute("aria-busy"), null);
  assert.strictEqual(button.children[0], glyph);
  assert.strictEqual(button.children[1], label);
  restore();
  assert.deepEqual(button.children, [glyph, label]);
  assert.equal(pendingButtonLabel("Delete project"), "Deleting…");
});

test("openModal filters absent optional regions instead of appending undefined", () => {
  const modal = new FakeElement("dialog");
  const context = {
    modal,
    actionOwner: { begin: () => ({ owns: () => true }) },
    cancelExit() {},
    clear: clearFake,
    closeModal() {},
    icon: fakeIcon,
    el(tag, props, ...children) {
      const node = new FakeElement(tag, props || {});
      node.append(...children.flat().filter((child) => child !== null && child !== undefined && child !== false));
      return node;
    },
  };
  const openModal = extractRange(
    "function openModal(",
    "/* Mobile drawer.",
    "openModal",
    context,
  );
  const footer = new FakeElement("footer");
  assert.equal(openModal({ title: "Confirm", footer, ownership: { owns: () => true } }), true);
  assert.equal(modal.children.length, 2);
  assert.strictEqual(modal.children[1], footer);
  assert.equal(modal.children.includes("undefined"), false);
});

test("mobile drawer applies dialog and inert lifecycle, traps focus, and controls restoration", () => {
  const sidebar = new FakeElement("aside");
  const scrim = new FakeElement("div", { hidden: true });
  const topbar = new FakeElement("header");
  const main = new FakeElement("main");
  const opener = new FakeElement("button");
  const current = new FakeElement("a");
  const last = new FakeElement("button");
  sidebar.append(current, last);

  const body = new FakeElement("body");
  const document = { body, activeElement: opener, querySelector: () => null };
  for (const node of [opener, current, last, sidebar]) {
    node.focus = () => { document.activeElement = node; node.focused = true; };
  }
  sidebar.querySelector = (selector) => selector.includes("aria-current") ? current : null;
  sidebar.querySelectorAll = () => [current, last];

  const media = { matches: true };
  const map = new Map([["#sidebar", sidebar], [".scrim", scrim], [".topbar", topbar]]);
  const context = {
    window: { matchMedia: () => media },
    document,
    host: () => main,
    $: (selector) => map.get(selector) || opener,
    $$: () => [opener],
    cancelExit() {},
    playExit(node, _ms, done) { done(); },
    EXIT: { scrim: 250 },
  };
  const drawer = extract(
    "DRAWER_LIFECYCLE_BEGIN",
    "DRAWER_LIFECYCLE_END",
    "({ openNav, closeNav, trapNavFocus })",
    context,
  );

  drawer.openNav({ currentTarget: opener });
  assert.equal(body.classList.contains("nav-open"), true);
  assert.equal(opener.getAttribute("aria-expanded"), "true");
  assert.equal(sidebar.getAttribute("role"), "dialog");
  assert.equal(sidebar.getAttribute("aria-modal"), "true");
  assert.equal(topbar.inert, true);
  assert.equal(main.inert, true);
  assert.strictEqual(document.activeElement, current);

  document.activeElement = last;
  let prevented = false;
  assert.equal(drawer.trapNavFocus({ key: "Tab", shiftKey: false, preventDefault() { prevented = true; } }), true);
  assert.equal(prevented, true);
  assert.strictEqual(document.activeElement, current);

  drawer.closeNav({ restoreFocus: false });
  assert.equal(topbar.inert, false);
  assert.equal(main.inert, false);
  assert.equal(sidebar.getAttribute("role"), null);
  assert.notStrictEqual(document.activeElement, opener);

  drawer.openNav({ currentTarget: opener });
  drawer.closeNav();
  assert.strictEqual(document.activeElement, opener);
  assert.equal(opener.getAttribute("aria-expanded"), "false");
});

test("collection create actions select exactly one permitted CTA", () => {
  const make = () => ({ cta: true });
  let selected = collectionCreateActions(false, false, true, make);
  assert.equal(selected.header, null);
  assert.ok(selected.empty);

  selected = collectionCreateActions(true, false, true, make);
  assert.ok(selected.header);
  assert.equal(selected.empty, null);

  selected = collectionCreateActions(false, true, true, make);
  assert.equal(selected.header, null);
  assert.equal(selected.empty, null);

  selected = collectionCreateActions(true, false, false, make);
  assert.equal(selected.header, null);
  assert.equal(selected.empty, null);
});

test("palette modifier label follows the platform while retaining both shortcut handlers", () => {
  assert.equal(paletteModifierLabel("MacIntel"), "⌘");
  assert.equal(paletteModifierLabel("Linux x86_64"), "Ctrl");
  assert.match(appSource, /e\.metaKey \|\| e\.ctrlKey/);
  assert.match(htmlSource, /id="palette-modifier">Ctrl\/⌘<\/kbd>/);
});

test("modernized source retains CSP-safe DOM, mobile palette, list, and layout invariants", () => {
  assert.match(htmlSource, /id="palette-mobile" aria-label="Open command palette"/);
  assert.match(appSource, /\$\("#palette-mobile"\)\.addEventListener\("click", openPalette\)/);
  assert.match(cssSource, /\.topbar__menu, \.topbar__palette \{ display: inline-grid; \}/);
  assert.match(cssSource, /\.palette__input input \{[^}]*min-width: 0;/);
  assert.match(appSource, /const wrap = el\("ul", \{ class: "feed" \}\)/);
  assert.match(appSource, /wrap\.append\(el\("li", \{ class: "feed__item" \}/);
  assert.match(appSource, /roster = members\.length \? el\("ul", \{ class: "stack" \}\)/);
  assert.match(appSource, /roster\.append\(el\("li", \{ class: "feed__item" \}/);
  assert.match(appSource, /const notifications = el\("ul", \{\}\)/);
  assert.match(appSource, /notifications\.append\(el\("li", \{ class: `notif/);
  assert.match(cssSource, /\.callout--warn \{[^}]*var\(--warn-bg\)/);
  assert.match(cssSource, /\.callout--err \{[^}]*var\(--err-bg\)/);
  assert.match(appSource, /detail\.classList\.add\("callout--warn"\)/);
  assert.match(appSource, /detail\.classList\.add\("callout--err"\)/);
  assert.doesNotMatch(appSource, /\.(?:innerHTML|outerHTML)\s*=/);
  assert.doesNotMatch(appSource, /insertAdjacentHTML|document\.write\s*\(/);
  assert.doesNotMatch(appSource, /\bstyle\s*:\s*["'`]/);
});

test("concurrent refreshes share one promise and finally permits a later rotation", async () => {
  let calls = 0;
  let pending = deferred();
  const rotate = createSingleflight(() => {
    calls += 1;
    return pending.promise;
  });

  const first = rotate();
  const second = rotate();
  assert.strictEqual(first, second);
  await Promise.resolve();
  assert.equal(calls, 1);
  pending.resolve("rotated");
  assert.equal(await first, "rotated");
  assert.equal(await second, "rotated");

  pending = deferred();
  const third = rotate();
  await Promise.resolve();
  assert.equal(calls, 2);
  pending.resolve("again");
  assert.equal(await third, "again");
});

test("failed singleflight clears itself instead of creating a refresh storm", async () => {
  let calls = 0;
  const rotate = createSingleflight(async () => {
    calls += 1;
    throw new Error("offline");
  });
  await assert.rejects(Promise.all([rotate(), rotate()]), /offline/);
  assert.equal(calls, 1);
  await assert.rejects(rotate(), /offline/);
  assert.equal(calls, 2);
});

test("concurrent same-session 401s rotate once and retry each request at most once", async () => {
  let refreshCalls = 0;
  const authorizations = new Map();
  const harness = authHarness(async (url, options) => {
    if (url.endsWith("/auth/refresh")) {
      refreshCalls += 1;
      return response(200, { access_token: "new-a", refresh_token: "new-r" });
    }
    const seen = authorizations.get(url) || [];
    seen.push(options.headers.Authorization);
    authorizations.set(url, seen);
    return options.headers.Authorization === "Bearer old-a"
      ? response(401, null, { code: "UNAUTHORIZED", message: "expired" })
      : response(200, { ok: true });
  });

  const results = await Promise.all([
    harness.api.call("/one", { method: "POST", body: { value: 1 } }),
    harness.api.call("/two"),
  ]);
  assert.equal(refreshCalls, 1);
  assert.deepEqual(results.map((item) => item.ok), [true, true]);
  assert.deepEqual(authorizations.get("/api/v1/one"), ["Bearer old-a", "Bearer new-a"]);
  assert.deepEqual(authorizations.get("/api/v1/two"), ["Bearer old-a", "Bearer new-a"]);
  assert.equal(harness.signOutCalls(), 0);
});

test("a repeated same-session 401 is bounded to one refresh, one retry, and one sign-out", async () => {
  let refreshCalls = 0;
  let resourceCalls = 0;
  const harness = authHarness(async (url) => {
    if (url.endsWith("/auth/refresh")) {
      refreshCalls += 1;
      return response(200, { access_token: "new-a", refresh_token: "new-r" });
    }
    resourceCalls += 1;
    return response(401, null, { code: "UNAUTHORIZED", message: "still unauthorized" });
  });

  await assert.rejects(harness.api.call("/resource"), (err) => err.status === 401);
  assert.equal(refreshCalls, 1);
  assert.equal(resourceCalls, 2);
  assert.equal(harness.signOutCalls(), 1);
});

test("a delayed old 401 cannot replay its POST body with a replacement login", async () => {
  const pending = deferred();
  const authorizations = [];
  const harness = authHarness(async (url, options) => {
    if (url.endsWith("/resource")) {
      authorizations.push(options.headers.Authorization);
      return pending.promise;
    }
    throw new Error(`unexpected fetch ${url}`);
  });

  const request = harness.api.call("/resource", { method: "POST", body: { destructive: true } });
  harness.context.sessionEpoch += 1;
  harness.state.access = "replacement-a";
  harness.state.refresh = "replacement-r";
  pending.resolve(response(401, null, { code: "UNAUTHORIZED", message: "old token" }));

  await assert.rejects(request, (err) => err.name === "AbortError");
  assert.deepEqual(authorizations, ["Bearer old-a"]);
  assert.equal(harness.state.access, "replacement-a");
  assert.equal(harness.state.refresh, "replacement-r");
  assert.equal(harness.signOutCalls(), 0);
});

test("an old refresh completing after sign-out and new login cannot clear the new session", async () => {
  const pendingRefresh = deferred();
  let refreshCalls = 0;
  const harness = authHarness(async (url) => {
    if (url.endsWith("/auth/refresh")) {
      refreshCalls += 1;
      return pendingRefresh.promise;
    }
    return response(401, null, { code: "UNAUTHORIZED", message: "expired" });
  });

  const request = harness.api.call("/resource");
  while (refreshCalls === 0) await new Promise((resolve) => setImmediate(resolve));
  harness.context.sessionEpoch += 2;
  harness.state.access = "replacement-a";
  harness.state.refresh = "replacement-r";
  pendingRefresh.resolve(response(200, { access_token: "stale-a", refresh_token: "stale-r" }));

  await assert.rejects(request, (err) => err.name === "AbortError");
  assert.equal(refreshCalls, 1);
  assert.equal(harness.state.access, "replacement-a");
  assert.equal(harness.state.refresh, "replacement-r");
  assert.equal(harness.signOutCalls(), 0);
});

test("newest view generation aborts and suppresses stale ownership", () => {
  const owner = createGenerationOwner();
  const oldView = owner.begin();
  const newView = owner.begin();
  assert.equal(oldView.signal.aborted, true);
  assert.equal(oldView.owns(), false);
  assert.equal(newView.signal.aborted, false);
  assert.equal(newView.owns(), true);

  const rendered = [];
  if (oldView.owns()) rendered.push("old");
  if (newView.owns()) rendered.push("new");
  assert.deepEqual(rendered, ["new"]);
});

test("honest optional loads distinguish forbidden, empty success, and failure", async () => {
  let called = false;
  const forbidden = await honestLoad(false, async () => { called = true; });
  assert.equal(forbidden.kind, "forbidden");
  assert.equal(called, false);

  const empty = await honestLoad(true, async () => []);
  assert.equal(empty.kind, "ready");
  assert.deepEqual(Array.from(empty.value), []);

  const failed = await honestLoad(true, async () => { throw new Error("timeout"); });
  assert.equal(failed.kind, "error");
  assert.match(failed.error.message, /timeout/);
});

test("fetchAllPages follows metadata, preserves every record, and enforces its cap", async () => {
  const requested = [];
  const all = Array.from({ length: 205 }, (_, id) => ({ id }));
  const fetchPage = async (url) => {
    requested.push(url);
    const page = Number(new URL(url, "https://example.test").searchParams.get("page"));
    const start = (page - 1) * 100;
    return {
      items: all.slice(start, start + 100),
      page: { page, page_size: 100, total: all.length, total_pages: 3 },
    };
  };

  const result = await fetchAllPages("/users?status=active", { fetchPage });
  assert.equal(result.items.length, 205);
  assert.deepEqual(Array.from(result.items, (item) => item.id), all.map((item) => item.id));
  assert.deepEqual(requested, [
    "/users?status=active&page=1&page_size=100",
    "/users?status=active&page=2&page_size=100",
    "/users?status=active&page=3&page_size=100",
  ]);

  await assert.rejects(
    fetchAllPages("/users", {
      cap: 1000,
      fetchPage: async () => ({ items: [], page: { page: 1, page_size: 100, total: 1001, total_pages: 11 } }),
    }),
    /more than 1000 records/,
  );
});

test("formModal uses native form submission and validity semantics", () => {
  const start = appSource.indexOf("function formModal(");
  const end = appSource.indexOf("function confirmModal(", start);
  const source = appSource.slice(start, end);
  assert.match(source, /el\("form", \{ class: "modal__form" \}/);
  assert.match(source, /type: "submit"/);
  assert.match(source, /form\.addEventListener\("submit"/);
  assert.match(source, /form\.checkValidity\(\)/);
  assert.match(source, /form\.reportValidity\(\)/);
  assert.doesNotMatch(source, /go\.addEventListener\("click"/);
  assert.doesNotMatch(htmlSource, /<form[^>]*novalidate/);
});

test("request retry guards and async modal actions retain explicit ownership", () => {
  assert.match(appSource, /res\.status === 401 && auth && !retried/);
  assert.match(appSource, /requestEpoch !== sessionEpoch/);
  assert.match(appSource, /return call\(path, opts, true, requestEpoch\)/);
  assert.match(appSource, /const rotate = createSingleflight/);
  assert.match(appSource, /rotationEpoch !== sessionEpoch \|\| state\.refresh !== refreshUsed/);

  const membersStart = appSource.indexOf("async function membersSheet");
  const membersEnd = appSource.indexOf("/* --- Members & invitations", membersStart);
  const membersSource = appSource.slice(membersStart, membersEnd);
  assert.match(membersSource, /const ownership = actionOwner\.begin\(\)/);
  assert.match(membersSource, /signal: ownership\.signal/);
  assert.match(membersSource, /if \(!ownership\.owns\(\)\) return/);
  assert.match(membersSource, /body, ownership/);

  const rolesStart = appSource.indexOf("async function rolePerms");
  const rolesEnd = appSource.indexOf("/* --- API keys", rolesStart);
  const rolesSource = appSource.slice(rolesStart, rolesEnd);
  assert.match(rolesSource, /signal: ownership\.signal/);
  assert.match(rolesSource, /if \(!ownership\.owns\(\)\) return false/);
  assert.match(appSource, /setTimeout\(\(\) => \{ if \(ownership\.owns\(\)\) showInvite/);
  assert.match(appSource, /setTimeout\(\(\) => \{ if \(ownership\.owns\(\)\) showSecret/);
});

test("stale action AbortErrors neither open UI nor surface an error", async () => {
  const owner = createGenerationOwner();
  const oldAction = owner.begin();
  const load = deferred();
  let opened = 0;
  let surfaced = 0;
  const work = load.promise.then(() => {
    if (oldAction.owns()) opened += 1;
  }).catch((err) => {
    if (oldAction.owns() && err.name !== "AbortError") surfaced += 1;
  });

  owner.begin();
  const aborted = new Error("aborted");
  aborted.name = "AbortError";
  load.reject(aborted);
  await work;
  assert.equal(oldAction.signal.aborted, true);
  assert.equal(opened, 0);
  assert.equal(surfaced, 0);
});


test("route invalidation cancels a pending palette command before it can open UI", async () => {
  const routeStart = appSource.indexOf("function route()");
  const routeEnd = appSource.indexOf("/* ────────────────────────────── Identity", routeStart);
  const routeSource = appSource.slice(routeStart, routeEnd);
  assert.match(routeSource, /paletteOwner\.invalidate\(\)/);

  const owner = createGenerationOwner();
  const ownership = owner.begin();
  const load = deferred();
  let opened = 0;
  const command = load.promise.then(() => {
    if (ownership.owns()) opened += 1;
  });

  // route() performs this invalidation before dispatching the next page.
  owner.invalidate();
  load.resolve();
  await command;
  assert.equal(ownership.signal.aborted, true);
  assert.equal(opened, 0);
});

test("actual modal catch paths silently ignore AbortError and stale ownership", async () => {
  const aborted = new Error("session replaced");
  aborted.name = "AbortError";
  assert.equal(shouldSurfaceAsyncError(aborted), false);

  let formOwned = true;
  const formOwnership = { owns: () => formOwned };
  const form = modalHarness();
  form.formModal({
    title: "Test",
    desc: "Test",
    submit: "Save",
    fields: [],
    ownership: formOwnership,
    onSubmit: async () => { throw aborted; },
  });
  const formNode = form.opened().body;
  const alert = formNode.children[0].children[0];
  await formNode.dispatch("submit", { preventDefault() {} });
  assert.equal(alert.hidden, true);
  assert.equal(alert.textContent, "");
  assert.equal(form.closeCalls(), 0);

  const stale = modalHarness();
  let staleOwned = true;
  const staleOwnership = { owns: () => staleOwned };
  stale.formModal({
    title: "Test",
    desc: "Test",
    submit: "Save",
    fields: [],
    ownership: staleOwnership,
    onSubmit: async () => {
      staleOwned = false;
      throw new Error("late failure");
    },
  });
  const staleForm = stale.opened().body;
  const staleAlert = staleForm.children[0].children[0];
  await staleForm.dispatch("submit", { preventDefault() {} });
  assert.equal(staleAlert.hidden, true);
  assert.equal(stale.closeCalls(), 0);

  const confirm = modalHarness();
  const confirmOwnership = { owns: () => true };
  confirm.confirmModal({
    title: "Delete?",
    desc: "Test",
    confirm: "Delete",
    ownership: confirmOwnership,
    onConfirm: async () => { throw aborted; },
  });
  const confirmButton = confirm.opened().footer.children[1];
  await confirmButton.dispatch("click");
  assert.deepEqual(confirm.toasts, []);
  assert.equal(confirm.closeCalls(), 0);
  assert.equal(confirmButton.disabled, false);
});

test("auth submit and retry catch paths explicitly suppress AbortError", () => {
  const retryStart = appSource.indexOf("function showSessionRetry");
  const retryEnd = appSource.indexOf("async function enter", retryStart);
  assert.match(appSource.slice(retryStart, retryEnd), /isAbort\(retryErr\)/);

  const authStart = appSource.indexOf("function wireAuth");
  const authEnd = appSource.indexOf("function wireShell", authStart);
  const authSource = appSource.slice(authStart, authEnd);
  assert.equal((authSource.match(/if \(isAbort\(err\)\) return;/g) || []).length, 2);
  assert.match(authSource, /if \(!isAbort\(err\)\) authMsg/);
});
