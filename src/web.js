import express from "express";
import { config } from "./config.js";
import { findApiKeyByToken, getDb, createApiKey, listDraftsForAccount } from "./db.js";
import { readSessionCookie, createSessionCookie, clearSessionCookie } from "./web-auth.js";
import {
  renderSignIn,
  renderDashboard,
  renderDraftDetail,
  renderCliAuth,
  renderCliAuthKey,
  renderError
} from "./render-web.js";
import { getDraftDetail, setDraftStatus, addComment, getCurrentVersion } from "./db.js";

// Server-rendered web UI. No build step: plain inline-styled HTML, like
// postplan's own dashboard. Sign-in is a paste-your-API-key flow so the whole
// product works without external OAuth.

export function registerWebRoutes(app) {
  app.use(webMiddlewares);

  app.get("/dashboard", (req, res) => {
    const session = readSessionCookie(req);
    if (!session) {
      return res.type("html").send(renderSignIn({ error: null, next: "/dashboard" }));
    }
    const drafts = listDraftsForAccount(session.accountId);
    const teams = getDb()
      .prepare(
        `SELECT t.* FROM teams t JOIN team_members tm ON tm.team_id = t.id WHERE tm.account_id = ?`
      )
      .all(session.accountId);
    res.type("html").send(renderDashboard({ session, drafts, teams }));
  });

  app.post("/dashboard/session", (req, res) => {
    if (!config.sessionSecret) {
      return res
        .status(503)
        .type("html")
        .send(renderError({ message: "Web sign-in is not configured (set SESSION_SECRET)." }));
    }
    const key = String(req.body?.apiKey || "").trim();
    const auth = key ? findApiKeyByToken(key) : null;
    if (!auth) {
      return res
        .status(401)
        .type("html")
        .send(renderSignIn({ error: "That API key was rejected. Generate one with `draftdeck auth set` or the CLI setup page.", next: "/dashboard" }));
    }
    res.append("Set-Cookie", createSessionCookie({ accountId: auth.account_id, accountName: auth.account_name, email: null }));
    res.redirect("/dashboard");
  });

  app.post("/auth/sign-out", (req, res) => {
    res.append("Set-Cookie", clearSessionCookie());
    res.redirect("/dashboard");
  });

  app.get("/dashboard/drafts/:draftId", (req, res) => {
    const session = requireSession(req, res);
    if (!session) return;
    const detail = getDraftDetail(req.params.draftId);
    if (!detail) return res.status(404).type("html").send(renderError({ message: "Draft not found." }));
    if (!canView(detail.draft, session)) {
      return res.status(403).type("html").send(renderError({ message: "You don't have access to this draft." }));
    }
    res.type("html").send(renderDraftDetail({ session, detail }));
  });

  app.post("/dashboard/drafts/:draftId/comments", (req, res) => {
    const session = requireSession(req, res);
    if (!session) return;
    const draft = getDb().prepare("SELECT * FROM drafts WHERE id = ? AND deleted_at IS NULL").get(req.params.draftId);
    if (!draft) return res.status(404).type("html").send(renderError({ message: "Draft not found." }));
    if (!canView(draft, session)) {
      return res.status(403).type("html").send(renderError({ message: "You don't have access to this draft." }));
    }
    const body = String(req.body?.body || "").trim().slice(0, 4000);
    if (!body) {
      return res.redirect(`/dashboard/drafts/${draft.id}?error=empty-comment`);
    }
    const current = getCurrentVersion(draft.id);
    const anchor = req.body?.anchor ? safeAnchor(req.body.anchor) : null;
    addComment({
      draftId: draft.id,
      versionNumber: Number(current?.version_number || 1),
      anchor,
      body,
      author: session.accountName || "Anonymous"
    });
    if (draft.status !== "approved") setDraftStatus(draft.id, "in_review");
    res.redirect(`/dashboard/drafts/${draft.id}#comments`);
  });

  app.post("/dashboard/drafts/:draftId/status", (req, res) => {
    const session = requireSession(req, res);
    if (!session) return;
    const draft = getDb().prepare("SELECT * FROM drafts WHERE id = ? AND deleted_at IS NULL").get(req.params.draftId);
    if (!draft) return res.status(404).type("html").send(renderError({ message: "Draft not found." }));
    if (!canView(draft, session)) {
      return res.status(403).type("html").send(renderError({ message: "You don't have access to this draft." }));
    }
    const status = String(req.body?.status || "");
    const allowed = new Set(["draft", "in_review", "changes_requested", "approved"]);
    if (allowed.has(status)) setDraftStatus(draft.id, status);
    res.redirect(`/dashboard/drafts/${draft.id}`);
  });

  app.get("/cli/auth", (req, res) => {
    const session = requireSession(req, res);
    if (!session) return;
    const keys = getDb()
      .prepare("SELECT id, name, created_at, last_used_at FROM api_keys WHERE account_id = ? AND revoked_at IS NULL ORDER BY created_at DESC")
      .all(session.accountId);
    res.type("html").send(renderCliAuth({ session, keys }));
  });

  app.post("/cli/auth/keys", (req, res) => {
    const session = requireSession(req, res);
    if (!session) return;
    const name = `CLI · ${new Date().toISOString().slice(0, 10)}`;
    const { id, token } = createApiKey(session.accountId, name);
    res.type("html").send(renderCliAuthKey({ session, token, keyName: name, keyId: id }));
  });
}

function webMiddlewares(req, res, next) {
  // Only dashboard/CLI POSTs need JSON parsing; draft URLs must never be
  // intercepted by dashboard middleware.
  if (req.method === "POST") {
    return express.json({ limit: "1mb" })(req, res, next);
  }
  next();
}

function requireSession(req, res) {
  const session = readSessionCookie(req);
  if (!session) {
    res.status(401).type("html").send(renderSignIn({ error: "Sign in to continue.", next: req.originalUrl }));
    return null;
  }
  return session;
}

function canView(draft, session) {
  if (draft.account_id && draft.account_id === session.accountId) return true;
  if (draft.team_id) {
    const row = getDb().prepare("SELECT 1 FROM team_members WHERE team_id = ? AND account_id = ?").get(draft.team_id, session.accountId);
    if (row) return true;
  }
  return false;
}

function safeAnchor(value) {
  try {
    const parsed = typeof value === "string" ? JSON.parse(value) : value;
    if (typeof parsed !== "object" || parsed === null) return null;
    const out = {};
    if (typeof parsed.selector === "string") out.selector = parsed.selector.slice(0, 500);
    if (Number.isFinite(parsed.x)) out.x = Math.round(parsed.x);
    if (Number.isFinite(parsed.y)) out.y = Math.round(parsed.y);
    if (typeof parsed.note === "string") out.note = parsed.note.slice(0, 200);
    return Object.keys(out).length ? out : null;
  } catch {
    return null;
  }
}

export function readSession(req) {
  return readSessionCookie(req);
}
