#!/usr/bin/env node

/**
 * install.js — Downloads the correct mem-mcp binary for the current platform
 * from the GitHub release.
 *
 * The binary is written to npm/bin/<platform-asset> so it never collides with
 * the wrapper script at npm/mem-mcp. On install, the wrapper script at
 * npm/mem-mcp resolves the real binary at runtime.
 *
 * If the download fails, the install exits with a non-zero code but does not
 * remove the package — the user can build manually or retry with `npm rebuild`.
 */

"use strict";

const { createWriteStream, unlinkSync, existsSync, chmodSync, mkdirSync } = require("fs");
const { get } = require("https");
const { platform, arch } = require("os");
const { join } = require("path");
const { assetFor } = require("./platforms");

const PACKAGE = "@fullstack-ai-infra/mem-mcp";
const REPO = "fullstack-ai-infra/mem";

// Version from package.json — single source of truth.
// eslint-disable-next-line import/no-unresolved
const { version: VERSION } = require("./package.json");

/**
 * Downloads a URL to a file, following HTTP redirects (up to 5 hops).
 * GitHub Release asset URLs 302 to objects.githubusercontent.com.
 */
function download(url, dest, redirects = 0) {
  return new Promise((resolve, reject) => {
    if (redirects > 5) {
      return reject(new Error("Too many redirects"));
    }
    const file = createWriteStream(dest);
    get(url, (res) => {
      // Follow redirects.
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        file.close();
        unlinkSync(dest);
        const redirectUrl = new URL(res.headers.location, url).toString();
        return download(redirectUrl, dest, redirects + 1).then(resolve, reject);
      }
      if (res.statusCode !== 200) {
        reject(new Error(`Download failed: HTTP ${res.statusCode}`));
        return;
      }
      res.pipe(file);
      file.on("finish", () => {
        file.close();
        resolve();
      });
    }).on("error", (err) => {
      // Clean up partial download.
      try { unlinkSync(dest); } catch (_) { /* ignore */ }
      reject(err);
    });
  });
}

async function main() {
  // Write to bin/ subdirectory so we never collide with the wrapper script at
  // npm/mem-mcp (which is checked into the package and resolves the binary here).
  const binDir = join(__dirname, "bin");
  const asset = assetFor(platform(), arch());
  const binPath = join(binDir, asset);
  const url = `https://github.com/${REPO}/releases/download/v${VERSION}/${asset}`;

  // Skip if already downloaded (reinstall, or cached).
  if (existsSync(binPath)) {
    console.log(`${PACKAGE}: binary already exists at ${binPath}`);
    return;
  }

  // Ensure bin/ directory exists.
  mkdirSync(binDir, { recursive: true });

  console.log(`${PACKAGE}: downloading ${url}...`);
  try {
    await download(url, binPath);
    chmodSync(binPath, 0o755);
    console.log(`${PACKAGE}: installed binary at ${binPath}`);
  } catch (err) {
    // Clean up partial download.
    if (existsSync(binPath)) unlinkSync(binPath);
    console.error(`${PACKAGE}: failed to download binary: ${err.message}`);
    console.error(`${PACKAGE}: you can build manually from https://github.com/${REPO}`);
    process.exit(1);
  }
}

main();