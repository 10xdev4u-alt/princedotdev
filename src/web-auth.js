import { createHmac, timingSafeEqual } from "node:crypto";
import { config } from "./config.js";

export const SESSION_COOKIE = "draftdeck_session";
const SESSION_TTL_SECONDS = 30 * 24 * 60 * 60;

// Stateless HMAC-signed tokens (base64url(JSON payload) + "." + HMAC-SHA256),
// same shape postplan uses. Nothing to store server-side; a restart
// invalidates nothing.
export function signToken(payload, secret, ttlSeconds) {
  const body = Buffer.from(
    JSON.stringify({ ...payload, exp: nowSeconds() + ttlSeconds })
  ).toString("base64url");
  return `${body}.${hmac(body, secret)}`;
}

export function verifyToken(token, secret) {
  if (typeof token !== "string" || !token.includes(".")) return null;
  const [body, signature] = token.split(".");
  const expected = hmac(body, secret);
  const a = Buffer.from(signature || "");
  const b = Buffer.from(expected);
  if (a.length !== b.length || !timingSafeEqual(a, b)) return null;

  try {
    const payload = JSON.parse(Buffer.from(body, "base64url").toString("utf8"));
    if (!Number.isFinite(payload.exp) || payload.exp < nowSeconds()) return null;
    return payload;
  } catch {
    return null;
  }
}

export function createSessionCookie({ accountId, accountName, email }) {
  const token = signToken(
    { accountId, accountName, email: email ?? null },
    requireSecret(),
    SESSION_TTL_SECONDS
  );
  return serializeCookie(SESSION_COOKIE, token, { maxAge: SESSION_TTL_SECONDS });
}

export function clearSessionCookie() {
  return serializeCookie(SESSION_COOKIE, "", { maxAge: 0 });
}

export function readSessionCookie(req) {
  if (!config.sessionSecret) return null;
  const token = readCookie(req, SESSION_COOKIE);
  if (!token) return null;
  const payload = verifyToken(token, config.sessionSecret);
  return payload?.accountId ? payload : null;
}

function readCookie(req, name) {
  const header = req.get("cookie") || "";
  for (const part of header.split(";")) {
    const eq = part.indexOf("=");
    if (eq === -1) continue;
    if (part.slice(0, eq).trim() === name) {
      try {
        return decodeURIComponent(part.slice(eq + 1).trim());
      } catch {
        return null;
      }
    }
  }
  return null;
}

function serializeCookie(name, value, { maxAge }) {
  const attributes = [
    `${name}=${encodeURIComponent(value)}`,
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    `Max-Age=${maxAge}`
  ];
  if (process.env.NODE_ENV !== "development") attributes.push("Secure");
  return attributes.join("; ");
}

function hmac(value, secret) {
  return createHmac("sha256", secret).update(value).digest("base64url");
}

function requireSecret() {
  if (!config.sessionSecret) {
    throw new Error("SESSION_SECRET is not configured.");
  }
  return config.sessionSecret;
}

function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}
