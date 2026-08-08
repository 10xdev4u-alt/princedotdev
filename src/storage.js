import fs from "node:fs";
import path from "node:path";
import { config } from "./config.js";

// Filesystem-backed object store, keyed like S3 (drafts/<id>/versions/<v>.html)
// so swapping in S3 later is a one-file change. Drafts are stored byte-for-byte.
function draftsDir() {
  const dir = path.join(config.dataDir, "drafts");
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  return dir;
}

export function putHtmlObject(key, html) {
  const full = path.join(draftsDir(), key);
  fs.mkdirSync(path.dirname(full), { recursive: true, mode: 0o700 });
  fs.writeFileSync(full, html, "utf8");
}

export function getHtmlObject(key) {
  const full = path.join(draftsDir(), key);
  return fs.readFileSync(full, "utf8");
}

export function deleteObject(key) {
  const full = path.join(draftsDir(), key);
  fs.rmSync(full, { force: true });
}
