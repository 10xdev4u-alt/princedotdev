import path from "node:path";
import os from "node:os";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const projectRoot = path.resolve(__dirname, "..");

export const config = {
  port: Number(process.env.PORT || 4000),
  // Where the sqlite db + draft files live. Defaults to ./data in the project
  // dir so `npm start` just works; point DATA_DIR elsewhere for deploys.
  dataDir: path.resolve(process.env.DATA_DIR || path.join(projectRoot, "data")),
  publicBaseUrl: (process.env.PUBLIC_BASE_URL || `http://localhost:4000`).replace(/\/+$/, ""),
  // Set to enable the web dashboard's paste-a-key sign-in and minting keys
  // from the browser (mirrors postplan's POSTPLAN_SESSION_SECRET).
  sessionSecret: process.env.SESSION_SECRET,
  // Identity assumed for anonymous (no-Bearer) uploads and viewers.
  anonymousAccountName: "Anonymous",
  maxHtmlBytes: Number(process.env.MAX_HTML_BYTES || 512 * 1024),
  uploadRateLimitMax: Number(process.env.UPLOAD_RATE_LIMIT_MAX || 30),
  uploadRateLimitWindowMs: Number(process.env.UPLOAD_RATE_LIMIT_WINDOW_MS || 60_000)
};

export function requireEnv(name, value) {
  if (!value) {
    throw new Error(`Missing required environment variable: ${name}`);
  }
  return value;
}
