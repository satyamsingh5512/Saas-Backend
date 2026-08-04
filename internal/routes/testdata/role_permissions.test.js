"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const appPath = path.join(__dirname, "..", "web", "assets", "app.js");
const appSource = fs.readFileSync(appPath, "utf8");

function loadValidator() {
  const beginMarker = "// ROLE_PERMISSION_EDITOR_VALIDATION_BEGIN";
  const endMarker = "// ROLE_PERMISSION_EDITOR_VALIDATION_END";
  const begin = appSource.indexOf(beginMarker);
  const end = appSource.indexOf(endMarker);
  assert.notEqual(begin, -1, "validator begin marker is missing");
  assert.notEqual(end, -1, "validator end marker is missing");
  assert.ok(end > begin, "validator markers are out of order");

  const context = {};
  vm.runInNewContext(
    `${appSource.slice(begin + beginMarker.length, end)}\nthis.validator = validateRolePermissionEditorData;`,
    context,
  );
  return context.validator;
}

const validate = loadValidator();

function role() {
  return { id: "role-1", name: "Release Manager" };
}

function grants(codes = ["project:view"]) {
  return { role_id: "role-1", permission_codes: codes, revision: "2026-07-30T18:31:02Z" };
}

test("fresh catalog represents a grant absent from the stale page catalog", () => {
  const stalePageCatalog = [];
  const freshCatalog = [{ code: "project:view", description: "View projects" }];
  assert.equal(stalePageCatalog.some((permission) => permission.code === "project:view"), false);

  const state = validate(role(), grants(), freshCatalog);
  assert.deepEqual(Array.from(state.initialCodes), ["project:view"]);
  assert.deepEqual(Array.from(state.catalog, (permission) => permission.code), ["project:view"]);
});

test("editor refuses an authoritative grant absent from the fresh catalog", () => {
  assert.throws(
    () => validate(role(), grants(["new:permission"]), [{ code: "project:view" }]),
    /cannot be represented exactly/,
  );
});

test("editor refuses mismatched roles, duplicate grants, and malformed catalogs", () => {
  assert.throws(
    () => validate(role(), { ...grants(), role_id: "role-2" }, [{ code: "project:view" }]),
    /mismatched role data/,
  );
  assert.throws(
    () => validate(role(), grants(["project:view", "project:view"]), [{ code: "project:view" }]),
    /cannot be represented exactly/,
  );
  assert.throws(
    () => validate(role(), grants(), [{ code: "project:view" }, { code: "project:view" }]),
    /catalog cannot be represented exactly/,
  );
});

test("existing-role editor fetches a fresh catalog and never receives the page catalog", () => {
  assert.match(appSource, /rolePerms\(r\)/);
  assert.doesNotMatch(appSource, /rolePerms\(r,\s*catalog\)/);
  assert.match(appSource, /const freshCatalogRequest = call\("\/permissions", \{ signal: ownership\.signal \}\)/);
  assert.match(appSource, /Promise\.all\(\[/);
  assert.match(appSource, /options: editorData\.catalog/);
});
