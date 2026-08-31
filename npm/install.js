#!/usr/bin/env node

/**
 * install.js — Downloads and verifies the mem-mcp binary for this platform.
 *
 * The checksum manifest and binary must come from the same GitHub Release.
 * A binary is written to its final executable path only after its SHA-256
 * digest matches the manifest entry for the exact platform asset.
 */

"use strict";

const { createHash, randomBytes, timingSafeEqual } = require("crypto");
const {
  chmodSync,
  createReadStream,
  createWriteStream,
  lstatSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} = require("fs");
const { get } = require("https");
const { arch, homedir, hostname, platform } = require("os");
const path = require("path");
const { pipeline } = require("stream/promises");
const { TextDecoder } = require("util");
const { assetFor } = require("./platforms");

const PACKAGE = "@fullstack-ai-infra/mem-mcp";
const REPO = "fullstack-ai-infra/mem";
const CHECKSUM_ASSET = "mem-mcp-checksums.txt";
const MAX_CHECKSUM_BYTES = 64 * 1024;
const MAX_REDIRECTS = 5;
const REQUEST_TIMEOUT_MS = 60 * 1000;
const LOCK_WAIT_TIMEOUT_MS = 120 * 1000;
const LOCK_STALE_MS = 10 * 60 * 1000;
const LOCK_ORPHAN_GRACE_MS = 5 * 1000;
const LOCK_POLL_MS = 50;
const guardedResponses = new WeakSet();

// Version from package.json — single source of truth for both release URLs.
// eslint-disable-next-line import/no-unresolved
const { version: VERSION } = require("./package.json");

function removeIfPresent(path) {
  try {
    unlinkSync(path);
  } catch (err) {
    if (err.code !== "ENOENT") throw err;
  }
}

function pathEntryExists(target) {
  try {
    lstatSync(target);
    return true;
  } catch (err) {
    if (err.code === "ENOENT") return false;
    throw err;
  }
}

function abortError(signal) {
  const reason = signal && signal.reason;
  const message = reason instanceof Error && reason.message
    ? reason.message
    : "mem-mcp bootstrap aborted";
  const error = new Error(message);
  error.name = "AbortError";
  return error;
}

function throwIfAborted(signal) {
  if (signal && signal.aborted) throw abortError(signal);
}

function removeOwnedPath(target) {
  try {
    const info = lstatSync(target);
    if (info.isDirectory() && !info.isSymbolicLink()) {
      rmSync(target, { recursive: true, force: true });
    } else {
      unlinkSync(target);
    }
  } catch (err) {
    if (err.code !== "ENOENT") throw err;
  }
}

function pathApiFor(osPlatform) {
  return osPlatform === "win32" ? path.win32 : path.posix;
}

function absolutePath(value, osPlatform, label) {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`${label} must be a non-empty absolute path`);
  }
  if (!pathApiFor(osPlatform).isAbsolute(value)) {
    throw new Error(`${label} must be an absolute path: ${value}`);
  }
  return value;
}

function cacheRootFor(options = {}) {
  const osPlatform = options.osPlatform || platform();
  const environment = options.environment || process.env;
  const homeDirectory = options.homeDirectory || homedir();
  const pathApi = pathApiFor(osPlatform);
  const override = environment.MEM_MCP_CACHE_DIR;

  if (override !== undefined) {
    return absolutePath(override, osPlatform, "MEM_MCP_CACHE_DIR");
  }

  if (osPlatform === "win32") {
    if (environment.LOCALAPPDATA) {
      return pathApi.join(
        absolutePath(environment.LOCALAPPDATA, osPlatform, "LOCALAPPDATA"),
        "fullstack-ai-infra",
        "mem-mcp",
      );
    }
    return pathApi.join(
      absolutePath(homeDirectory, osPlatform, "home directory"),
      "AppData",
      "Local",
      "fullstack-ai-infra",
      "mem-mcp",
    );
  }

  if (osPlatform === "darwin") {
    return pathApi.join(
      absolutePath(homeDirectory, osPlatform, "home directory"),
      "Library",
      "Caches",
      "fullstack-ai-infra",
      "mem-mcp",
    );
  }

  if (environment.XDG_CACHE_HOME) {
    return pathApi.join(
      absolutePath(environment.XDG_CACHE_HOME, osPlatform, "XDG_CACHE_HOME"),
      "fullstack-ai-infra",
      "mem-mcp",
    );
  }
  return pathApi.join(
    absolutePath(homeDirectory, osPlatform, "home directory"),
    ".cache",
    "fullstack-ai-infra",
    "mem-mcp",
  );
}

function safeVersion(version) {
  if (!/^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`Unsafe package version for cache path: ${version}`);
  }
  return version;
}

function cacheDirectory(options = {}) {
  const osPlatform = options.osPlatform || platform();
  const osArch = options.osArch || arch();
  const version = safeVersion(options.version || VERSION);
  const pathApi = pathApiFor(osPlatform);
  const root = cacheRootFor({
    osPlatform,
    environment: options.environment,
    homeDirectory: options.homeDirectory,
  });
  return pathApi.join(root, `v${version}`, `${osPlatform}-${osArch}`);
}

function ensureCacheDirectory(cacheDir) {
  const hostPlatform = platform();
  absolutePath(cacheDir, hostPlatform, "mem-mcp cache directory");
  mkdirSync(cacheDir, { recursive: true, mode: 0o700 });
  const info = lstatSync(cacheDir);
  if (info.isSymbolicLink() || !info.isDirectory()) {
    throw new Error(`Unsafe mem-mcp cache directory: ${cacheDir}`);
  }
  if (hostPlatform !== "win32") chmodSync(cacheDir, 0o700);
}

function delay(milliseconds, signal) {
  throwIfAborted(signal);
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      if (signal) signal.removeEventListener("abort", onAbort);
      resolve();
    }, milliseconds);
    const onAbort = () => {
      clearTimeout(timeout);
      reject(abortError(signal));
    };
    if (signal) {
      signal.addEventListener("abort", onAbort, { once: true });
      if (signal.aborted) onAbort();
    }
  });
}

function readLockOwner(lockPath) {
  try {
    return JSON.parse(readFileSync(path.join(lockPath, "owner.json"), "utf8"));
  } catch (_) {
    return null;
  }
}

function ownerProcessIsAlive(owner) {
  if (
    !owner ||
    owner.hostname !== hostname() ||
    !Number.isSafeInteger(owner.pid) ||
    owner.pid <= 0
  ) {
    return null;
  }
  try {
    process.kill(owner.pid, 0);
    return true;
  } catch (err) {
    return err.code === "ESRCH" ? false : true;
  }
}

function ownedArtifactPaths(cacheDir, asset, nonce) {
  if (!/^[0-9a-f]{24}$/.test(nonce)) return [];
  return [
    path.join(cacheDir, `.${asset}.${nonce}.tmp`),
    path.join(cacheDir, `.${asset}.${nonce}.invalid`),
    path.join(cacheDir, `.${asset}.${nonce}.failed`),
  ];
}

function reclaimStaleLock(lockPath, cacheDir, asset, staleMs, orphanGraceMs) {
  let info;
  try {
    info = lstatSync(lockPath);
  } catch (err) {
    if (err.code === "ENOENT") return true;
    throw err;
  }
  if (info.isSymbolicLink() || !info.isDirectory()) {
    throw new Error(`Unsafe mem-mcp cache lock: ${lockPath}`);
  }

  const owner = readLockOwner(lockPath);
  const age = Math.max(0, Date.now() - info.mtimeMs);
  const alive = ownerProcessIsAlive(owner);
  const reclaimable = alive === false
    ? age >= orphanGraceMs
    : alive === true
      ? false
      : age >= staleMs;
  if (!reclaimable) return false;

  const quarantine = `${lockPath}.stale.${process.pid}.${randomBytes(12).toString("hex")}`;
  try {
    renameSync(lockPath, quarantine);
  } catch (err) {
    if (err.code === "ENOENT" || err.code === "EEXIST") return true;
    throw err;
  }

  if (owner && typeof owner.nonce === "string") {
    for (const ownedPath of ownedArtifactPaths(cacheDir, asset, owner.nonce)) {
      removeOwnedPath(ownedPath);
    }
  }
  removeOwnedPath(quarantine);
  return true;
}

async function acquireAssetLock(cacheDir, asset, options = {}) {
  const waitTimeoutMs = options.waitTimeoutMs ?? LOCK_WAIT_TIMEOUT_MS;
  const staleMs = options.staleMs ?? LOCK_STALE_MS;
  const orphanGraceMs = options.orphanGraceMs ?? LOCK_ORPHAN_GRACE_MS;
  const pollMs = options.pollMs ?? LOCK_POLL_MS;
  const signal = options.signal;
  const lockPath = path.join(cacheDir, `.${asset}.lock`);
  const deadline = Date.now() + waitTimeoutMs;

  for (;;) {
    throwIfAborted(signal);
    const nonce = randomBytes(12).toString("hex");
    try {
      mkdirSync(lockPath, { mode: 0o700 });
      try {
        writeFileSync(
          path.join(lockPath, "owner.json"),
          `${JSON.stringify({ pid: process.pid, hostname: hostname(), nonce })}\n`,
          { flag: "wx", mode: 0o600 },
        );
      } catch (err) {
        removeOwnedPath(lockPath);
        throw err;
      }
      return { lockPath, nonce };
    } catch (err) {
      if (err.code !== "EEXIST") throw err;
    }

    if (reclaimStaleLock(lockPath, cacheDir, asset, staleMs, orphanGraceMs)) {
      continue;
    }
    if (Date.now() >= deadline) {
      throw new Error(`Timed out waiting for mem-mcp cache lock: ${lockPath}`);
    }
    await delay(Math.max(1, pollMs), signal);
  }
}

function releaseAssetLock(lock) {
  const owner = readLockOwner(lock.lockPath);
  if (!owner || owner.nonce !== lock.nonce || owner.pid !== process.pid) {
    throw new Error(`Refusing to release a mem-mcp cache lock not owned by this process`);
  }
  removeOwnedPath(lock.lockPath);
}

function checkedHttpsUrl(value, base) {
  const parsed = new URL(value, base);
  if (parsed.protocol !== "https:") {
    throw new Error(`Refusing non-HTTPS download URL: ${parsed.toString()}`);
  }
  return parsed;
}

/**
 * Stops an untrusted response body that the caller will not consume.
 *
 * IncomingMessage may emit an error after the status callback returns (for
 * example, when a redirect body is reset). Keep an error listener attached so
 * an intentionally discarded body cannot escape the install failure path as
 * an uncaught exception.
 */
function guardResponseErrors(response) {
  if (guardedResponses.has(response)) return;
  guardedResponses.add(response);
  // This prevents an error emitted between the HTTPS status callback and the
  // consumer's pipeline/async iterator from becoming an uncaught exception.
  // Stream consumers still receive the same error through their own listener
  // or the Readable's stored errored state and therefore reject normally.
  response.on("error", () => {});
}

function discardResponse(response) {
  guardResponseErrors(response);
  response.destroy();
}

/** Opens an HTTPS response, following at most five redirects. */
function openResponse(url, redirects = 0, requestGet = get, options = {}) {
  return new Promise((resolve, reject) => {
    const signal = options.signal;
    const timeoutMs = options.timeoutMs ?? REQUEST_TIMEOUT_MS;
    if (signal && signal.aborted) {
      reject(abortError(signal));
      return;
    }

    let parsed;
    try {
      parsed = checkedHttpsUrl(url);
    } catch (err) {
      reject(err);
      return;
    }

    let request;
    let response = null;
    let timeout = null;
    let finished = false;
    const cleanup = () => {
      if (finished) return;
      finished = true;
      if (timeout !== null) clearTimeout(timeout);
      if (signal) signal.removeEventListener("abort", onAbort);
    };
    const interrupt = (err) => {
      cleanup();
      if (response && !response.destroyed) response.destroy(err);
      if (request && typeof request.destroy === "function") request.destroy(err);
      reject(err);
    };
    const onAbort = () => interrupt(abortError(signal));
    if (signal) signal.addEventListener("abort", onAbort, { once: true });

    try {
      request = requestGet(
        parsed,
        {
          headers: { "User-Agent": `${PACKAGE}/${VERSION}` },
          signal,
        },
        (incoming) => {
          response = incoming;
          guardResponseErrors(incoming);
          const status = incoming.statusCode || 0;
          if (status >= 300 && status < 400 && incoming.headers.location) {
            discardResponse(incoming);
            cleanup();
            if (redirects >= MAX_REDIRECTS) {
              reject(new Error("Too many redirects"));
              return;
            }
            let next;
            try {
              next = checkedHttpsUrl(incoming.headers.location, parsed);
            } catch (err) {
              reject(err);
              return;
            }
            openResponse(next, redirects + 1, requestGet, options).then(resolve, reject);
            return;
          }
          if (status !== 200) {
            discardResponse(incoming);
            cleanup();
            reject(new Error(`Download failed: HTTP ${status}`));
            return;
          }
          incoming.once("end", cleanup);
          incoming.once("close", cleanup);
          resolve(incoming);
        },
      );
    } catch (err) {
      cleanup();
      reject(err);
      return;
    }
    request.once("error", (err) => {
      cleanup();
      reject(err);
    });
    if (!finished) {
      timeout = setTimeout(() => {
        interrupt(new Error(`Download timed out after ${timeoutMs} ms`));
      }, timeoutMs);
    }
    if (signal && signal.aborted) onAbort();
  });
}

async function downloadFile(url, destination, options = {}) {
  const response = await openResponse(url, 0, get, options);
  await pipeline(
    response,
    createWriteStream(destination, { flags: "wx", mode: 0o600 }),
    { signal: options.signal },
  );
}

async function downloadText(
  url,
  maxBytes = MAX_CHECKSUM_BYTES,
  open = openResponse,
  options = {},
) {
  const response = await open(url, 0, get, options);
  const declaredLength = Number(response.headers["content-length"] || 0);
  if (Number.isFinite(declaredLength) && declaredLength > maxBytes) {
    discardResponse(response);
    throw new Error(`Checksum manifest exceeds ${maxBytes} bytes`);
  }

  const chunks = [];
  let size = 0;
  for await (const chunk of response) {
    throwIfAborted(options.signal);
    size += chunk.length;
    if (size > maxBytes) {
      discardResponse(response);
      throw new Error(`Checksum manifest exceeds ${maxBytes} bytes`);
    }
    chunks.push(chunk);
  }
  throwIfAborted(options.signal);
  return new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks));
}

/**
 * Returns the unique lowercase SHA-256 hex digest for an exact asset name.
 * Every manifest row must use the output format emitted by GNU sha256sum:
 * 64 lowercase hexadecimal characters, two spaces, then one safe basename.
 */
function checksumForAsset(manifest, asset) {
  if (typeof manifest !== "string" || !manifest.endsWith("\n")) {
    throw new Error("Checksum manifest must be non-empty and newline-terminated");
  }
  const lines = manifest.slice(0, -1).split("\n");
  if (lines.length === 0 || lines.some((line) => line.length === 0)) {
    throw new Error("Checksum manifest contains an empty line");
  }

  let expected = null;
  for (const [index, line] of lines.entries()) {
    const match = /^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)$/.exec(line);
    if (!match) {
      throw new Error(`Invalid checksum manifest line ${index + 1}`);
    }
    if (match[2] === asset) {
      if (expected !== null) {
        throw new Error(`Duplicate checksum entry for ${asset}`);
      }
      expected = match[1];
    }
  }
  if (expected === null) {
    throw new Error(`Checksum manifest has no entry for ${asset}`);
  }
  return expected;
}

async function sha256File(path, signal) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path, { signal })) {
    throwIfAborted(signal);
    hash.update(chunk);
  }
  return hash.digest();
}

async function verifyFile(path, expectedHex, signal) {
  throwIfAborted(signal);
  if (!/^[0-9a-f]{64}$/.test(expectedHex)) {
    throw new Error("Expected checksum is not lowercase SHA-256 hex");
  }
  const before = lstatSync(path);
  if (before.isSymbolicLink() || !before.isFile()) {
    throw new Error(`Cached binary is not a regular file: ${path}`);
  }
  const expected = Buffer.from(expectedHex, "hex");
  const actual = await sha256File(path, signal);
  throwIfAborted(signal);
  const after = lstatSync(path);
  if (
    after.isSymbolicLink() ||
    !after.isFile() ||
    before.dev !== after.dev ||
    before.ino !== after.ino ||
    before.size !== after.size ||
    before.mtimeMs !== after.mtimeMs
  ) {
    throw new Error(`Cached binary changed during verification: ${path}`);
  }
  if (!timingSafeEqual(actual, expected)) {
    throw new Error(
      `Checksum mismatch: expected ${expectedHex}, got ${actual.toString("hex")}`,
    );
  }
}

function quarantineCacheEntry(binPath, cacheDir, asset, nonce, suffix) {
  if (suffix !== "invalid" && suffix !== "failed") {
    throw new Error(`Unsafe mem-mcp quarantine suffix: ${suffix}`);
  }
  const quarantinePath = path.join(cacheDir, `.${asset}.${nonce}.${suffix}`);
  renameSync(binPath, quarantinePath);
  const info = lstatSync(quarantinePath);
  if (!info.isSymbolicLink() && info.isFile() && platform() !== "win32") {
    try {
      chmodSync(quarantinePath, 0o600);
    } catch (err) {
      try {
        removeOwnedPath(quarantinePath);
      } catch (cleanupError) {
        throw new AggregateError(
          [err, cleanupError],
          `${err.message}; failed to remove quarantined cache entry`,
        );
      }
      throw err;
    }
  }
  return quarantinePath;
}

async function install(options = {}) {
  const osPlatform = options.osPlatform || platform();
  const osArch = options.osArch || arch();
  const version = safeVersion(options.version || VERSION);
  const repository = options.repository || REPO;
  const environment = options.environment || process.env;
  const cacheDir = options.cacheDir !== undefined
    ? options.cacheDir
    : cacheDirectory({
      osPlatform,
      osArch,
      version,
      environment,
      homeDirectory: options.homeDirectory,
    });
  const fetchText = options.downloadText || downloadText;
  const fetchFile = options.downloadFile || downloadFile;
  const logger = options.logger || console;
  const signal = options.signal;
  const requestOptions = {
    signal,
    timeoutMs: options.requestTimeoutMs ?? REQUEST_TIMEOUT_MS,
  };

  const asset = assetFor(osPlatform, osArch);
  const binPath = path.join(cacheDir, asset);
  const releaseBase = `https://github.com/${repository}/releases/download/v${version}`;
  const checksumUrl = `${releaseBase}/${CHECKSUM_ASSET}`;
  const binaryUrl = `${releaseBase}/${asset}`;
  let lock = null;
  let tempPath = null;
  let quarantinePath = null;
  let failedServicePath = null;
  let servicePathVerified = false;
  let operationError = null;

  throwIfAborted(signal);
  ensureCacheDirectory(cacheDir);
  lock = await acquireAssetLock(cacheDir, asset, {
    waitTimeoutMs: options.lockWaitTimeoutMs,
    staleMs: options.lockStaleMs,
    orphanGraceMs: options.lockOrphanGraceMs,
    pollMs: options.lockPollMs,
    signal,
  });

  try {
    throwIfAborted(signal);
    logger.log(`${PACKAGE}: downloading ${checksumUrl}...`);
    const manifest = await fetchText(
      checksumUrl,
      MAX_CHECKSUM_BYTES,
      openResponse,
      requestOptions,
    );
    throwIfAborted(signal);
    const expected = checksumForAsset(manifest, asset);

    if (pathEntryExists(binPath)) {
      try {
        await verifyFile(binPath, expected, signal);
        chmodSync(binPath, 0o755);
        servicePathVerified = true;
        logger.log(`${PACKAGE}: verified existing binary at ${binPath}`);
        return binPath;
      } catch (err) {
        if (servicePathVerified) throw err;
        if ((signal && signal.aborted) || err.name === "AbortError") throw err;
        quarantinePath = quarantineCacheEntry(
          binPath,
          cacheDir,
          asset,
          lock.nonce,
          "invalid",
        );
        logger.warn(
          `${PACKAGE}: removed unverified cached binary from service path: ${err.message}`,
        );
      }
    }

    tempPath = path.join(cacheDir, `.${asset}.${lock.nonce}.tmp`);
    logger.log(`${PACKAGE}: downloading ${binaryUrl}...`);
    await fetchFile(binaryUrl, tempPath, requestOptions);
    throwIfAborted(signal);
    await verifyFile(tempPath, expected, signal);
    throwIfAborted(signal);
    chmodSync(tempPath, 0o755);
    renameSync(tempPath, binPath);
    tempPath = null;
    servicePathVerified = true;
    logger.log(`${PACKAGE}: installed verified binary at ${binPath}`);
    if (quarantinePath !== null) {
      removeOwnedPath(quarantinePath);
      quarantinePath = null;
    }
    return binPath;
  } catch (err) {
    operationError = err;
    const cleanupErrors = [];
    if (!servicePathVerified) {
      try {
        if (pathEntryExists(binPath)) {
          failedServicePath = quarantineCacheEntry(
            binPath,
            cacheDir,
            asset,
            lock.nonce,
            "failed",
          );
        }
      } catch (cleanupError) {
        cleanupErrors.push(cleanupError);
      }
    }
    if (tempPath !== null) {
      try {
        removeIfPresent(tempPath);
      } catch (cleanupError) {
        cleanupErrors.push(cleanupError);
      }
    }
    if (failedServicePath !== null) {
      try {
        removeOwnedPath(failedServicePath);
      } catch (cleanupError) {
        cleanupErrors.push(cleanupError);
      }
    }
    if (quarantinePath !== null) {
      try {
        removeOwnedPath(quarantinePath);
      } catch (cleanupError) {
        cleanupErrors.push(cleanupError);
      }
    }
    if (cleanupErrors.length > 0) {
      operationError = new AggregateError(
        [err, ...cleanupErrors],
        `${err.message}; failed to remove unverified install files`,
      );
      throw operationError;
    }
    throw err;
  } finally {
    try {
      releaseAssetLock(lock);
    } catch (lockError) {
      if (operationError !== null) {
        throw new AggregateError(
          [operationError, lockError],
          `${operationError.message}; failed to release cache lock`,
        );
      }
      throw lockError;
    }
  }
}

async function main() {
  try {
    await install();
  } catch (err) {
    console.error(`${PACKAGE}: failed to install verified binary: ${err.message}`);
    console.error(`${PACKAGE}: you can build manually from https://github.com/${REPO}`);
    process.exitCode = 1;
  }
}

if (require.main === module) {
  main();
}

module.exports = {
  acquireAssetLock,
  cacheDirectory,
  cacheRootFor,
  checksumForAsset,
  discardResponse,
  downloadFile,
  downloadText,
  ensureCacheDirectory,
  install,
  openResponse,
  releaseAssetLock,
  sha256File,
  verifyFile,
};
