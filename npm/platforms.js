#!/usr/bin/env node

/**
 * platforms.js — single source of truth for the mem-mcp release assets.
 *
 * Both install.js (checksum-verified bootstrap) and the `mem-mcp` bin wrapper
 * resolve the platform binary through this table, so the two cannot drift.
 * The keys are `<os>-<cpu>` as reported by Node's `os.platform()` and
 * `os.arch()`; the values must match the asset names built by
 * .github/workflows/release.yml exactly.
 */

"use strict";

const ASSETS = {
  "linux-x64": "mem-mcp-linux-amd64",
  "linux-arm64": "mem-mcp-linux-arm64",
  "darwin-x64": "mem-mcp-darwin-amd64",
  "darwin-arm64": "mem-mcp-darwin-arm64",
  "win32-x64": "mem-mcp-windows-amd64.exe",
  "win32-arm64": "mem-mcp-windows-arm64.exe",
};

/**
 * Returns the release asset name for a Node platform/cpu pair.
 *
 * @param {string} osPlatform value of `os.platform()`
 * @param {string} osArch value of `os.arch()`
 * @returns {string} asset name, e.g. `mem-mcp-windows-amd64.exe`
 * @throws {Error} when the host has no published binary
 */
function assetFor(osPlatform, osArch) {
  const key = `${osPlatform}-${osArch}`;
  const asset = ASSETS[key];
  if (!asset) {
    throw new Error(
      `Unsupported platform: ${key}. ${
        "@fullstack-ai-infra/mem-mcp"
      } publishes: ${Object.keys(ASSETS).join(", ")}.`
    );
  }
  return asset;
}

module.exports = { ASSETS, assetFor };
