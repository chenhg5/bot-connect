#!/usr/bin/env node
"use strict";

const { spawnSync } = require("child_process");
const fs = require("fs");
const path = require("path");

const binary = path.join(__dirname, "bin", "bot-connect");
if (!fs.existsSync(binary)) {
  // e.g. installed with --ignore-scripts
  const r = spawnSync(process.execPath, [path.join(__dirname, "install.js")], { stdio: "inherit" });
  if (r.status !== 0) process.exit(r.status || 1);
}
const r = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
if (r.error) {
  console.error(`[bot-connect] ${r.error.message}`);
  process.exit(1);
}
process.exit(r.status === null ? 1 : r.status);
