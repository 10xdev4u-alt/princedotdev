import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";

// Isolate the server in a temp data dir on an ephemeral port.
const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), "draftdeck-test-"));
process.env.DATA_DIR = dataDir;
process.env.PORT = "0";
process.env.PUBLIC_BASE_URL = "http://test.local";
process.env.SESSION_SECRET = "test-secret";

let server;
let baseUrl;
let key = "";

const SAMPLE_HTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Test Plan</title>
<style>body{font-family:sans-serif}</style>
</head>
<body><h1>Migration plan</h1><p>Phase 1: move the database.</p></body>
</html>`;

function req(url, { method = "GET", body, token } = {}) {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers.Authorization = `Bearer ${token}`;
  return fetch(`${baseUrl}${url}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(10_000)
  }).then(async (res) => ({
    status: res.status,
    body: await res.json().catch(() => null),
    headers: res.headers
  }));
}

before(async () => {
  server = spawn(process.execPath, ["src/server.js"], {
    cwd: path.resolve(import.meta.dirname, ".."),
    env: process.env,
    stdio: ["ignore", "pipe", "pipe"]
  });
  server.stderr.on("data", (d) => process.stderr.write(`[server] ${d}`));

  // Wait for the port. PORT=0 means the real port only appears in the log.
  baseUrl = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("server did not boot")), 10_000);
    server.stdout.on("data", (chunk) => {
      const match = chunk.toString().match(/listening on (http:\/\/[^\s]+)/);
      if (match) {
        clearTimeout(timer);
        resolve(match[1]);
      }
    });
  });

  // Create an account + key via the same code path as the create-user script.
  const { getDb, createAccount, createApiKey } = await import("../src/db.js");
  getDb();
  const accountId = createAccount("Tester", "tester@example.com");
  key = createApiKey(accountId, "test-key").token;
});

after(() => {
  server?.kill();
  fs.rmSync(dataDir, { recursive: true, force: true });
});

test("healthz", async () => {
  const res = await fetch(`${baseUrl}/healthz`);
  assert.equal(res.status, 200);
  assert.deepEqual(await res.json(), { ok: true });
});

test("anonymous upload + byte-for-byte raw serving", async () => {
  const res = await req("/api/uploads", { method: "POST", body: { html: SAMPLE_HTML, filename: "plan.html" } });
  assert.equal(res.status, 201);
  const data = res.body;
  assert.equal(data.ok, true);
  assert.equal(data.visibility, "unlisted");
  assert.match(data.draftId, /^[a-z0-9]{12}$/);
  assert.equal(data.versionNumber, 1);

  // Raw URL serves the exact bytes.
  const raw = await fetch(`${baseUrl}/d/${data.draftId}/raw`);
  assert.equal(raw.status, 200);
  assert.equal(await raw.text(), SAMPLE_HTML);
  assert.equal(raw.headers.get("x-draftdeck-draft-id"), data.draftId);
  assert.equal(raw.headers.get("x-draftdeck-draft-version"), "1");
  assert.match(raw.headers.get("content-security-policy"), /script-src 'none'/);

  // Versioned URL also serves bytes.
  const v1 = await fetch(`${baseUrl}/d/${data.draftId}/v/1/raw`);
  assert.equal(await v1.text(), SAMPLE_HTML);
});

test("re-upload same draft creates v2", async () => {
  const first = await req("/api/uploads", { method: "POST", body: { html: SAMPLE_HTML, filename: "plan.html" } });
  const draftId = first.body.draftId;

  const v2html = SAMPLE_HTML.replace("Phase 1", "Phase 2");
  const second = await req("/api/uploads", {
    method: "POST",
    body: { html: v2html, filename: "plan.html", draftId }
  });
  assert.equal(second.status, 200);
  assert.equal(second.body.versionNumber, 2);
  assert.equal(second.body.draftId, draftId);

  const raw = await fetch(`${baseUrl}/d/${draftId}/raw`);
  assert.equal(await raw.text(), v2html);
});

test("HTML policy blocks external scripts and forms", async () => {
  const bad = await req("/api/uploads", {
    method: "POST",
    body: {
      html: `<html><head><title>x</title></head><body><script src="https://evil.com/x.js"></script><form><input></form></body></html>`
    }
  });
  assert.equal(bad.status, 422);
  const errors = bad.body.errors.join("\n");
  assert.match(errors, /External script sources/);
  assert.match(errors, /Blocked <form>/);
});

test("team drafts require auth and membership", async () => {
  // Anonymous cannot create a team draft.
  const anon = await req("/api/uploads", {
    method: "POST",
    body: { html: SAMPLE_HTML, filename: "private.html", visibility: "team" }
  });
  assert.equal(anon.status, 401);

  // Authenticated user creates a team, then a team draft.
  const team = await req("/api/teams", { method: "POST", token: key, body: { name: "Eng" } });
  assert.equal(team.status, 201);
  const teamId = team.body.team.id;

  const upload = await req("/api/uploads", {
    method: "POST",
    token: key,
    body: { html: SAMPLE_HTML, filename: "private.html", teamId, visibility: "team" }
  });
  assert.equal(upload.status, 201);
  const draftId = upload.body.draftId;
  assert.equal(upload.body.visibility, "team");

  // Unauthenticated GET of a team draft is rejected.
  const anonView = await fetch(`${baseUrl}/d/${draftId}/raw`);
  assert.equal(anonView.status, 401);

  // Authenticated member CAN view it.
  const memberView = await fetch(`${baseUrl}/d/${draftId}/raw`, {
    headers: { Authorization: `Bearer ${key}` }
  });
  assert.equal(memberView.status, 200);
  assert.equal(await memberView.text(), SAMPLE_HTML);
});

test("comments + status workflow", async () => {
  const upload = await req("/api/uploads", { method: "POST", token: key, body: { html: SAMPLE_HTML, filename: "review.html" } });
  assert.equal(upload.status, 201);
  const draftId = upload.body.draftId;

  // Comment flips status to in_review.
  const comment = await req(`/api/drafts/${draftId}/comments`, {
    method: "POST",
    token: key,
    body: { body: "Please clarify phase 2", anchor: { selector: "#phase-2" } }
  });
  assert.equal(comment.status, 201);
  assert.equal(comment.body.comment.author, "Tester");
  assert.deepEqual(comment.body.comment.anchor, { selector: "#phase-2" });

  const after = await req(`/api/drafts/${draftId}`, { token: key });
  assert.equal(after.body.draft.status, "in_review");

  // Approve.
  const approved = await req(`/api/drafts/${draftId}/status`, { method: "POST", token: key, body: { status: "approved" } });
  assert.equal(approved.body.draft.status, "approved");

  // Agent pulls comments back via the JSON endpoint.
  const comments = await req(`/api/drafts/${draftId}/comments`, { token: key });
  assert.equal(comments.body.comments.length, 1);
  assert.equal(comments.body.comments[0].body, "Please clarify phase 2");

  // Re-upload after approval resets status to draft (change made).
  const re = await req("/api/uploads", {
    method: "POST",
    token: key,
    body: { html: SAMPLE_HTML.replace("Phase 1", "Phase 1 (clarified)"), filename: "review.html", draftId }
  });
  assert.equal(re.body.versionNumber, 2);
  assert.equal(re.body.status, "draft");

  // The reset is persisted, not just reported in the response.
  const persisted = await req(`/api/drafts/${draftId}`, { token: key });
  assert.equal(persisted.body.draft.status, "draft");
});

test("list endpoint returns own + team drafts only", async () => {
  const res = await req("/api/drafts", { token: key });
  assert.equal(res.status, 200);
  const titles = res.body.drafts.map((d) => d.title);
  assert.ok(titles.includes("Test Plan"));
  // No public drafts in the listing (they're reachable by URL only).
  const pub = await req("/api/uploads", { method: "POST", body: { html: SAMPLE_HTML, filename: "pub.html", visibility: "public" } });
  const after = await req("/api/drafts", { token: key });
  assert.ok(!after.body.drafts.some((d) => d.draftId === pub.body.draftId));
});
