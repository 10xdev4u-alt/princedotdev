import express from "express";
import { config } from "./config.js";
import { contentHash, sha256 } from "./crypto.js";
import { newDraftId, newInternalId } from "./ids.js";
import { validateHtml } from "./html-policy.js";
import { putHtmlObject, getHtmlObject } from "./storage.js";
import { createRateLimiter } from "./rate-limit.js";
import {
  anonymousAuth,
  findApiKeyByToken,
  getDb,
  createDraft,
  findDraft,
  getDraftDetail,
  listDraftsForAccount,
  addVersion,
  updateDraftCurrent,
  getVersion,
  getCurrentVersion,
  setDraftStatus,
  softDeleteDraft,
  addComment,
  createTeam,
  addTeamMember,
  isTeamMember,
  isTeamOwner,
  listTeamsForAccount,
  findTeam,
  listTeamDrafts,
  createApiKey
} from "./db.js";
import { renderHome, renderNotFound } from "./render.js";

const STATUSES = new Set(["draft", "in_review", "changes_requested", "approved"]);
const VISIBILITIES = new Set(["public", "unlisted", "team"]);

export function createApp() {
  const app = express();
  app.set("trust proxy", true);

  const uploadRateLimit = createRateLimiter({
    windowMs: config.uploadRateLimitWindowMs,
    max: config.uploadRateLimitMax,
    keyPrefix: "upload",
    key: (req) => (req.auth ? req.auth.id : req.ip || "anonymous")
  });

  // JSON body parsing scoped to /api so draft GETs with a stray JSON body can
  // never fail parsing instead of serving the draft HTML.
  app.use("/api", express.json({ limit: "2mb" }));
  app.use(noStoreHeaders);

  app.get("/", (req, res) => {
    res.type("html").send(renderHome({ publicBaseUrl: config.publicBaseUrl }));
  });

  app.get("/healthz", (req, res) => {
    try {
      getDb().prepare("SELECT 1").get();
      res.json({ ok: true });
    } catch (error) {
      res.status(503).json({ ok: false, error: error.message });
    }
  });

  // ---- API: identity ------------------------------------------------------

  app.get("/api/me", requireAuth, (req, res) => {
    res.json({
      accountId: req.auth.account_id,
      accountName: req.auth.account_name,
      apiKeyId: req.auth.id,
      apiKeyName: req.auth.name
    });
  });

  app.post("/api/api-keys", requireAuth, (req, res, next) => {
    try {
      const name = cleanText(req.body?.name) || "API Key";
      const { id, token } = createApiKey(req.auth.account_id, name);
      res.status(201).json({ ok: true, apiKey: { id, name }, token });
    } catch (error) {
      next(error);
    }
  });

  // ---- API: drafts ----------------------------------------------------------

  app.get("/api/drafts", requireAuth, (req, res, next) => {
    try {
      const drafts = listDraftsForAccount(req.auth.account_id);
      res.json({ ok: true, drafts });
    } catch (error) {
      next(error);
    }
  });

  // Full detail (metadata + versions + comments) for one draft. Unlike raw
  // serving, this always requires auth so anonymous clients use the raw URLs.
  app.get("/api/drafts/:draftId", requireAuth, (req, res, next) => {
    try {
      const detail = getDraftDetail(req.params.draftId);
      if (!detail) return res.status(404).json({ ok: false, error: "Draft not found." });
      if (!canAccess(detail.draft, req.auth)) {
        return res.status(403).json({ ok: false, error: "You don't have access to this draft." });
      }
      res.json({ ok: true, draft: decorateDraft(detail.draft), versions: detail.versions, comments: detail.comments.map(decorateComment) });
    } catch (error) {
      next(error);
    }
  });

  app.post("/api/uploads", optionalUploadAuth, uploadRateLimit, async (req, res, next) => {
    try {
      const { html, filename, draftId, description, metadata = {}, visibility, teamId } = req.body || {};
      const validation = validateHtml(html, { maxBytes: config.maxHtmlBytes });
      if (!validation.ok) {
        return res.status(422).json({ ok: false, errors: validation.errors, warnings: validation.warnings });
      }

      const isAnonymous = req.auth.id === anonymousAuth.id;
      const requestedVisibility = visibility || (teamId ? "team" : "unlisted");
      if (!VISIBILITIES.has(requestedVisibility)) {
        return res.status(400).json({ ok: false, error: `Invalid visibility "${requestedVisibility}".` });
      }
      if (requestedVisibility === "team" && isAnonymous) {
        return res.status(401).json({
          ok: false,
          error: "Team drafts require an API key. Run: draftdeck auth set <api-key>"
        });
      }
      if (teamId && !isTeamMember(teamId, req.auth.account_id)) {
        return res.status(403).json({ ok: false, error: "You're not a member of that team." });
      }

      const byteLength = Buffer.byteLength(html, "utf8");
      const versionId = `ver_${newInternalId()}`;
      const sourceIp = req.ip || null;
      const meta = {
        sourceIp,
        userAgent: req.get("user-agent"),
        cliVersion: metadata.cliVersion,
        gitBranch: metadata.gitBranch,
        gitCommitSha: metadata.gitCommitSha,
        gitCommitSubject: metadata.gitCommitSubject,
        gitDirty: typeof metadata.gitDirty === "boolean" ? metadata.gitDirty : null,
        originalFilename: cleanText(filename)
      };

      let draft;
      if (draftId) {
        const existing = findDraft(draftId);
        if (!existing || existing.deleted_at) {
          return res.status(404).json({ ok: false, error: "Draft not found." });
        }
        if (!canAccess(existing, req.auth)) {
          return res.status(403).json({ ok: false, error: "You don't have access to this draft." });
        }
        draft = existing;
        // A new version after approval means the agent incorporated feedback:
        // reset the status so the review cycle starts fresh (persisted, not
        // just in-memory).
        if (draft.status === "approved") {
          setDraftStatus(draft.id, "draft");
          draft.status = "draft";
        }
      } else {
        // Anonymous drafts are owned by the anonymous sentinel account, so the
        // CLI's update-by-file-path flow works without a key (the same draft
        // id is reused on re-upload).
        const accountId = isAnonymous ? anonymousAuth.account_id : req.auth.account_id;
        const newId = createDraft({
          accountId,
          teamId: teamId || null,
          title: validation.title || cleanText(filename) || "Untitled Draft",
          description: cleanText(description, 1000),
          visibility: requestedVisibility
        });
        draft = findDraft(newId);
      }

      const finalKey = `drafts/${draft.id}/versions/${versionId}.html`;
      putHtmlObject(finalKey, html);

      const version = addVersion({
        draftId: draft.id,
        versionId,
        objectKey: finalKey,
        contentHash: contentHash(html),
        fileSize: byteLength,
        apiKeyId: req.auth.id,
        meta
      });

      updateDraftCurrent(draft.id, version.id, {
        title: validation.title || cleanText(filename),
        description: cleanText(description, 1000)
      });

      res.status(draftId ? 200 : 201).json({
        ok: true,
        draftId: draft.id,
        versionId: version.id,
        versionNumber: Number(version.version_number),
        title: draft.title,
        visibility: draft.visibility,
        status: draft.status,
        publicUrl: `${config.publicBaseUrl}/d/${draft.id}`,
        rawUrl: `${config.publicBaseUrl}/d/${draft.id}/raw`,
        warnings: validation.warnings
      });
    } catch (error) {
      next(error);
    }
  });

  // ---- API: comments --------------------------------------------------------

  app.get("/api/drafts/:draftId/comments", optionalAuthMiddleware, (req, res, next) => {
    try {
      const draft = findDraft(req.params.draftId);
      if (!draft || draft.deleted_at) return res.status(404).json({ ok: false, error: "Draft not found." });
      if (draft.visibility === "team" && !(req.auth && isTeamMember(draft.team_id, req.auth.account_id))) {
        return res.status(403).json({ ok: false, error: "You don't have access to this draft." });
      }
      const detail = getDraftDetail(draft.id);
      res.json({ ok: true, comments: detail.comments.map(decorateComment) });
    } catch (error) {
      next(error);
    }
  });

  app.post("/api/drafts/:draftId/comments", requireAuth, (req, res, next) => {
    try {
      const draft = findDraft(req.params.draftId);
      if (!draft || draft.deleted_at) return res.status(404).json({ ok: false, error: "Draft not found." });
      if (draft.visibility === "team" && !isTeamMember(draft.team_id, req.auth.account_id)) {
        return res.status(403).json({ ok: false, error: "You don't have access to this draft." });
      }

      const body = cleanText(req.body?.body, 4000);
      if (!body) return res.status(400).json({ ok: false, error: "Comment body is required." });

      const current = getCurrentVersion(draft.id);
      const anchor = sanitizeAnchor(req.body?.anchor);
      const versionNumber = Number(req.body?.versionNumber) || Number(current?.version_number) || 1;

      const comment = addComment({
        draftId: draft.id,
        versionNumber,
        anchor,
        body,
        author: req.auth.account_name || "Anonymous"
      });

      // A new comment on a non-approved draft returns it to review.
      if (draft.status !== "approved") {
        setDraftStatus(draft.id, "in_review");
      }

      res.status(201).json({ ok: true, comment: decorateComment(comment) });
    } catch (error) {
      next(error);
    }
  });

  // ---- API: status workflow --------------------------------------------------

  app.post("/api/drafts/:draftId/status", requireAuth, (req, res, next) => {
    try {
      const draft = findDraft(req.params.draftId);
      if (!draft || draft.deleted_at) return res.status(404).json({ ok: false, error: "Draft not found." });
      if (!canAccess(draft, req.auth)) {
        return res.status(403).json({ ok: false, error: "You don't have access to this draft." });
      }
      const status = req.body?.status;
      if (!STATUSES.has(status)) {
        return res.status(400).json({ ok: false, error: `Invalid status "${status}".` });
      }
      const updated = setDraftStatus(draft.id, status);
      res.json({ ok: true, draft: decorateDraft(updated) });
    } catch (error) {
      next(error);
    }
  });

  app.delete("/api/drafts/:draftId", requireAuth, (req, res, next) => {
    try {
      const draft = findDraft(req.params.draftId);
      if (!draft || draft.deleted_at) return res.status(404).json({ ok: false, error: "Draft not found." });
      if (!canAccess(draft, req.auth)) {
        return res.status(403).json({ ok: false, error: "You don't have access to this draft." });
      }
      softDeleteDraft(draft.id);
      res.json({ ok: true });
    } catch (error) {
      next(error);
    }
  });


  // ---- Draft serving ----------------------------------------------------------
  // The raw HTML contract: /d/<id> and /d/<id>/raw serve the exact uploaded
  // bytes to every client. Team drafts require an authenticated member (API key
  // or web session).

  const serve = async (req, res, next) => {
    try {
      const draftId = req.params.draftId;
      const draft = findDraft(draftId);
      if (!draft || draft.deleted_at) return notFound(res);

      const versionNumber = req.params.versionNumber ? Number(req.params.versionNumber) : undefined;
      const version =
        versionNumber !== undefined
          ? getVersion(draft.id, versionNumber)
          : getCurrentVersion(draft.id);

      if (!version) return notFound(res);


      const html = getHtmlObject(version.object_key);
      res.setHeader("Content-Security-Policy", draftCsp());
      res.setHeader("X-Draftdeck-Draft-Id", draft.id);
      res.setHeader("X-Draftdeck-Draft-Version", String(Number(version.version_number)));
      res.setHeader("X-Draftdeck-Draft-Status", draft.status);
      res.type("html").send(html);
    } catch (error) {
      next(error);
    }
  };

  app.get(["/d/:draftId", "/d/:draftId/raw"], serve);
  app.get(["/d/:draftId/v/:versionNumber", "/d/:draftId/v/:versionNumber/raw"], serve);

  app.use((req, res) => {
    res.status(404).type("html").send(renderNotFound());
  });

  app.use((error, req, res, _next) => {
    const status = error.statusCode || 500;
    const message = status >= 500 ? "Internal server error." : error.message;
    if (status >= 500) console.error(error);
    res.status(status).json({ ok: false, error: message });
  });

  return app;
}

// ---- helpers ----------------------------------------------------------------

function draftCsp() {
  return [
    "default-src 'none'",
    "script-src 'none'",
    "script-src-attr 'none'",
    "style-src 'unsafe-inline'",
    "img-src https: data:",
    "connect-src 'none'",
    "worker-src 'none'",
    "frame-src 'none'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'none'"
  ].join("; ");
}

function notFound(res) {
  return res.status(404).type("html").send(renderNotFound());
}

function renderNeedAuth() {
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Team draft</title>
<style>body{font-family:ui-sans-serif,system-ui,sans-serif;background:#f8fafc;color:#111827;display:grid;place-items:center;min-height:100vh;margin:0}p{color:#6b7280;text-align:center;max-width:420px;line-height:1.6}</style>
</head><body><main><h1>Private team draft</h1><p>This draft is visible only to ${"members of its team"}. Sign in with an API key to view it.</p></main></body></html>`;
}

// Who may view/edit a draft: the owning account, or any member of the team.
function canAccess(draft, auth) {
  if (!auth) return false;
  if (draft.account_id && draft.account_id === auth.account_id) return true;
  if (draft.team_id && isTeamMember(draft.team_id, auth.account_id)) return true;
  return false;
}

function decorateDraft(draft) {
  const current = getCurrentVersion(draft.id);
  return {
    draftId: draft.id,
    title: draft.title,
    description: draft.description,
    visibility: draft.visibility,
    status: draft.status,
    teamId: draft.team_id,
    repoOrg: draft.repo_org,
    repoName: draft.repo_name,
    latestVersionNumber: current ? Number(current.version_number) : null,
    createdAt: draft.created_at,
    updatedAt: draft.updated_at,
    publicUrl: `${config.publicBaseUrl}/d/${draft.id}`,
    rawUrl: `${config.publicBaseUrl}/d/${draft.id}/raw`
  };
}

function sanitizeAnchor(anchor) {
  if (!anchor || typeof anchor !== "object") return null;
  const out = {};
  if (typeof anchor.selector === "string" && anchor.selector.length <= 500) out.selector = anchor.selector;
  if (Number.isFinite(anchor.x)) out.x = Math.round(anchor.x);
  if (Number.isFinite(anchor.y)) out.y = Math.round(anchor.y);
  if (typeof anchor.note === "string" && anchor.note.length <= 200) out.note = anchor.note;
  return Object.keys(out).length ? out : null;
}

function decorateComment(comment) {
  if (!comment) return comment;
  let anchor = comment.anchor;
  if (typeof anchor === "string") {
    try {
      anchor = JSON.parse(anchor);
    } catch {
      anchor = null;
    }
  }
  return { ...comment, anchor: anchor || null };
}

function cleanText(value, maxLength = 255) {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  return trimmed ? trimmed.slice(0, maxLength) : null;
}

async function optionalUploadAuth(req, _res, next) {
  req.auth = (await optionalAuth(req)) || anonymousAuth;
  next();
}

// Middleware form of optionalAuth: sets req.auth (null when unauthenticated)
// and calls next(). A plain async helper that only returns a value would make
// Express 5 await a promise that never calls next() — the request hangs.
async function optionalAuthMiddleware(req, _res, next) {
  req.auth = await optionalAuth(req);
  next();
}

async function optionalAuth(req) {
  const header = req.get("authorization") || "";
  const match = header.match(/^Bearer\s+(.+)$/i);
  if (!match) return null;
  return findApiKeyByToken(match[1].trim());
}

function noStoreHeaders(req, res, next) {
  res.setHeader("X-Content-Type-Options", "nosniff");
  res.setHeader("Cache-Control", "no-store");
  next();
}

async function requireAuth(req, res, next) {
  const auth = await optionalAuth(req);
  if (!auth) {
    return res.status(401).json({ ok: false, error: "Missing or invalid API key." });
  }
  req.auth = auth;
  next();
}

