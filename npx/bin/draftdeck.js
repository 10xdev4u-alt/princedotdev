#!/usr/bin/env node
// draftdeck npx bootstrap — the esbuild pattern: this package is a tiny
// downloader; the real client is a single static Go binary fetched from
// GitHub Releases (or a self-hosted mirror via DRAFTDECK_BIN_BASE) on first
// run, then cached per platform.
"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const https = require("https");
const http = require("http");
const { spawnSync } = require("child_process");

const PKG_VERSION = require("../package.json").version;
const REPO = "10xdev4u-alt/princedotdev";
const BIN_NAME = "draftdeck";
// DRAFTDECK_BIN_BASE overrides the download root (useful for mirrors/tests).
const BIN_BASE = process.env.DRAFTDECK_BIN_BASE ||
  `https://github.com/${REPO}/releases/download/v${PKG_VERSION}`;

function platformTriple() {
  const osMap = { darwin: "darwin", linux: "linux", win32: "windows" };
  const archMap = { x64: "amd64", arm64: "arm64" };
  const o = osMap[process.platform];
  const a = archMap[process.arch];
  if (!o || !a) {
    throw new Error(
      `draftdeck: unsupported platform ${process.platform}-${process.arch}. ` +
      "Supported: linux/darwin/windows on amd64/arm64."
    );
  }
  return `${o}-${a}`;
}

function cacheDir() {
  if (process.env.DRAFTDECK_CACHE) return process.env.DRAFTDECK_CACHE;
  const home = os.homedir ? os.homedir() : os.tmpdir();
  const base = process.env.XDG_CACHE_HOME || path.join(home, ".cache");
  return path.join(base, "draftdeck");
}

function binPath() {
  const ext = process.platform === "win32" ? ".exe" : "";
  return path.join(cacheDir(), `${BIN_NAME}-${platformTriple()}${ext}`);
}

function fetch(url, dest) {
  return new Promise((resolve, reject) => {
    const mod = url.startsWith("https:") ? https : http;
    const req = mod.get(url, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        res.resume();
        return resolve(fetch(new URL(res.headers.location, url).toString(), dest));
      }
      if (res.statusCode !== 200) {
        res.resume();
        return reject(new Error(`HTTP ${res.statusCode} for ${url}`));
      }
      const file = fs.createWriteStream(dest);
      res.pipe(file);
      file.on("finish", () => file.close(resolve));
      file.on("error", reject);
    });
    req.on("error", reject);
    req.setTimeout(120000, () => req.destroy(new Error("download timed out")));
  });
}

async function ensureBinary() {
  const dest = binPath();
  if (fs.existsSync(dest)) return dest;
  const dir = path.dirname(dest);
  fs.mkdirSync(dir, { recursive: true });
  const url = `${BIN_BASE}/draftdeck-${platformTriple()}`;
  const tmp = `${dest}.tmp-${process.pid}`;
  console.error(`draftdeck: downloading ${url} …`);
  return fetch(url, tmp)
    .then(() => {
      fs.renameSync(tmp, dest);
      if (process.platform !== "win32") fs.chmodSync(dest, 0o755);
      return dest;
    })
    .catch((err) => {
      try { fs.unlinkSync(tmp); } catch {}
      throw new Error(`draftdeck: failed to download the client (${err.message}). ` +
        "Set DRAFTDECK_BIN_BASE to a mirror or install the binary manually.");
    });
}

ensureBinary().then((bin) => {
  const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
  process.exit(result.status === null ? 1 : result.status);
}).catch((err) => {
  console.error(err.message);
  process.exit(1);
});
