#!/usr/bin/env node
// Downloads the bot-connect binary for this platform from the GitHub release
// matching this package's version, verifies it against checksums.txt, and
// puts it in ./bin.
"use strict";

const { execFileSync } = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const https = require("https");
const os = require("os");
const path = require("path");

const REPO = "chenhg5/bot-connect";
const VERSION = "v" + require("./package.json").version;
const PLATFORMS = { darwin: "darwin", linux: "linux", win32: "windows" };
const ARCHS = { x64: "amd64", arm64: "arm64" };

function fetch(url, redirects = 5) {
  return new Promise((resolve, reject) => {
    https
      .get(url, { headers: { "User-Agent": "bot-connect-npm" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location && redirects > 0) {
          res.resume();
          return resolve(fetch(res.headers.location, redirects - 1));
        }
        if (res.statusCode !== 200) {
          res.resume();
          return reject(new Error(`HTTP ${res.statusCode} for ${url}`));
        }
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      })
      .on("error", reject);
  });
}

async function main() {
  const platform = PLATFORMS[process.platform];
  const arch = ARCHS[process.arch];
  if (!platform || !arch) {
    throw new Error(`bot-connect supports macOS, Linux and Windows on x64/arm64 (got ${process.platform}/${process.arch})`);
  }
  const name = `bot-connect-${VERSION}-${platform}-${arch}`;
  const ext = platform === "windows" ? ".zip" : ".tar.gz";
  const exe = platform === "windows" ? "bot-connect.exe" : "bot-connect";
  const base = `https://github.com/${REPO}/releases/download/${VERSION}`;
  const binDir = path.join(__dirname, "bin");
  const binary = path.join(binDir, exe);

  console.log(`[bot-connect] downloading ${name}${ext}`);
  const [archive, sums] = await Promise.all([fetch(`${base}/${name}${ext}`), fetch(`${base}/checksums.txt`)]);

  const line = sums.toString().split(/\r?\n/).find((l) => l.endsWith(` ${name}${ext}`));
  if (!line) throw new Error(`no checksum for ${name}${ext}`);
  const actual = crypto.createHash("sha256").update(archive).digest("hex");
  if (actual !== line.split(/\s+/)[0]) throw new Error(`checksum mismatch for ${name}${ext}`);

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "bot-connect-"));
  try {
    const file = path.join(tmp, "a" + ext);
    fs.writeFileSync(file, archive);
    // tar on Windows 10+ (bsdtar) also extracts .zip
    execFileSync("tar", ["-xf", file, "-C", tmp]);
    fs.mkdirSync(binDir, { recursive: true });
    fs.copyFileSync(path.join(tmp, name, exe), binary);
    fs.chmodSync(binary, 0o755);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
  console.log(`[bot-connect] installed ${VERSION}`);
}

main().catch((err) => {
  console.error(`[bot-connect] install failed: ${err.message}`);
  console.error(`[bot-connect] or download it from https://github.com/${REPO}/releases`);
  process.exit(1);
});
