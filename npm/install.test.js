"use strict";

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const { createHash } = require("node:crypto");
const { EventEmitter } = require("node:events");
const {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} = require("node:fs");
const { arch, hostname, platform, tmpdir } = require("node:os");
const { join } = require("node:path");
const { PassThrough } = require("node:stream");
const test = require("node:test");
const { assetFor } = require("./platforms");
const {
  cacheDirectory,
  cacheRootFor,
  checksumForAsset,
  downloadText,
  install,
  openResponse,
} = require("./install");

const ASSET = assetFor("linux", "x64");
const QUIET_LOGGER = { log() {}, warn() {} };

function digest(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function manifestFor(bytes, asset = ASSET) {
  return `${digest(bytes)}  ${asset}\n`;
}

function testDirectory(t) {
  const directory = mkdtempSync(join(tmpdir(), "mem-mcp-install-test-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  return directory;
}

function runWorker(t, program) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ["-e", program], {
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    t.after(() => {
      if (child.exitCode === null && child.signalCode === null) {
        child.kill("SIGKILL");
      }
    });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0 && signal === null) {
        resolve(stdout);
      } else {
        reject(new Error(
          `worker ${child.pid} failed: code=${code} signal=${signal}\n${stdout}${stderr}`,
        ));
      }
    });
  });
}

function fakeResponse(statusCode, headers = {}) {
  const response = new PassThrough();
  response.statusCode = statusCode;
  response.headers = headers;
  return response;
}

function fakeGetSequence(responses) {
  const pending = [...responses];
  return (_url, _options, callback) => {
    const request = new EventEmitter();
    const next = pending.shift();
    const response = next.response || next;
    queueMicrotask(() => {
      callback(response);
      if (next.afterCallback) next.afterCallback(response);
    });
    return request;
  };
}

test("checksumForAsset accepts one exact sha256sum row", () => {
  const wanted = Buffer.from("verified binary");
  const other = Buffer.from("other binary");
  const manifest = [
    `${digest(other)}  mem-mcp-darwin-arm64`,
    `${digest(wanted)}  ${ASSET}`,
  ].join("\n") + "\n";

  assert.equal(checksumForAsset(manifest, ASSET), digest(wanted));
});

test("checksumForAsset rejects malformed, missing, and duplicate rows", () => {
  const hash = digest(Buffer.from("binary"));
  const cases = [
    ["missing final newline", `${hash}  ${ASSET}`, /newline-terminated/],
    ["uppercase hex", `${hash.toUpperCase()}  ${ASSET}\n`, /Invalid/],
    ["single separator", `${hash} ${ASSET}\n`, /Invalid/],
    ["binary marker", `${hash} *${ASSET}\n`, /Invalid/],
    ["path instead of basename", `${hash}  .\/${ASSET}\n`, /Invalid/],
    ["missing asset", `${hash}  mem-mcp-darwin-arm64\n`, /no entry/],
    [
      "duplicate asset",
      `${hash}  ${ASSET}\n${hash}  ${ASSET}\n`,
      /Duplicate/,
    ],
  ];

  for (const [name, manifest, expected] of cases) {
    assert.throws(() => checksumForAsset(manifest, ASSET), expected, name);
  }
});

test("redirect body reset cannot escape as an uncaught stream error", async () => {
  const redirect = fakeResponse(302, { location: "https://assets.example/binary" });
  const success = fakeResponse(200);
  const responsePromise = openResponse(
    "https://github.example/release/binary",
    0,
    fakeGetSequence([redirect, success]),
  );

  await new Promise((resolve) => setImmediate(resolve));
  assert.doesNotThrow(() => redirect.emit("error", new Error("redirect reset")));
  assert.equal(await responsePromise, success);
  success.destroy();
});

test("200 response reset before consumer attachment rejects normally", async () => {
  const success = fakeResponse(200);
  const reset = new Error("reset immediately after 200 headers");
  const textPromise = downloadText(
    "https://github.example/release/checksums",
    64,
    () => openResponse(
      "https://github.example/release/checksums",
      0,
      fakeGetSequence([{
        response: success,
        afterCallback: (response) => {
          response.destroy(reset);
          // Model an error event emitted in the status-callback/consumer gap.
          response.emit("error", reset);
        },
      }]),
    ),
  );

  await assert.rejects(textPromise, /reset immediately after 200 headers/);
});

test("HTTP error body reset remains a controlled rejection", async () => {
  const unavailable = fakeResponse(503);
  const responsePromise = openResponse(
    "https://github.example/release/binary",
    0,
    fakeGetSequence([unavailable]),
  );

  await assert.rejects(responsePromise, /Download failed: HTTP 503/);
  assert.doesNotThrow(() => unavailable.emit("error", new Error("error reset")));
});

test("oversized manifest body reset remains a controlled rejection", async () => {
  const oversized = fakeResponse(200, { "content-length": "5" });
  const textPromise = downloadText(
    "https://github.example/release/checksums",
    4,
    async () => oversized,
  );

  await assert.rejects(textPromise, /exceeds 4 bytes/);
  assert.doesNotThrow(() => oversized.emit("error", new Error("oversize reset")));
});

test("a stalled HTTPS request rejects on the bounded download timeout", async () => {
  const request = new EventEmitter();
  request.destroy = (err) => queueMicrotask(() => request.emit("error", err));

  await assert.rejects(
    openResponse(
      "https://github.example/release/checksums",
      0,
      () => request,
      { timeoutMs: 10 },
    ),
    /Download timed out after 10 ms/,
  );
});

test("cache paths are user-scoped, versioned, and require absolute overrides", () => {
  assert.equal(
    cacheRootFor({
      osPlatform: "linux",
      environment: { MEM_MCP_CACHE_DIR: "/var/cache/mem-mcp-test" },
      homeDirectory: "/home/example",
    }),
    "/var/cache/mem-mcp-test",
  );
  assert.equal(
    cacheRootFor({
      osPlatform: "linux",
      environment: { XDG_CACHE_HOME: "/var/cache/example" },
      homeDirectory: "/home/example",
    }),
    "/var/cache/example/fullstack-ai-infra/mem-mcp",
  );
  assert.equal(
    cacheRootFor({
      osPlatform: "darwin",
      environment: {},
      homeDirectory: "/Users/example",
    }),
    "/Users/example/Library/Caches/fullstack-ai-infra/mem-mcp",
  );
  assert.equal(
    cacheRootFor({
      osPlatform: "win32",
      environment: { LOCALAPPDATA: "C:\\Users\\example\\AppData\\Local" },
      homeDirectory: "C:\\Users\\example",
    }),
    "C:\\Users\\example\\AppData\\Local\\fullstack-ai-infra\\mem-mcp",
  );
  assert.equal(
    cacheDirectory({
      osPlatform: "linux",
      osArch: "arm64",
      version: "0.1.1",
      environment: { MEM_MCP_CACHE_DIR: "/var/cache/mem-mcp-test" },
      homeDirectory: "/home/example",
    }),
    "/var/cache/mem-mcp-test/v0.1.1/linux-arm64",
  );
  assert.throws(
    () => cacheRootFor({
      osPlatform: "linux",
      environment: { MEM_MCP_CACHE_DIR: "relative/cache" },
      homeDirectory: "/home/example",
    }),
    /must be an absolute path/,
  );
  assert.throws(
    () => cacheRootFor({
      osPlatform: "linux",
      environment: { XDG_CACHE_HOME: "relative/cache" },
      homeDirectory: "/home/example",
    }),
    /must be an absolute path/,
  );
});

test("install verifies a temporary download before exposing it", async (t) => {
  const root = testDirectory(t);
  const cacheRoot = join(root, "user-cache");
  const osPlatform = platform();
  const osArch = arch();
  const asset = assetFor(osPlatform, osArch);
  const cacheDir = join(cacheRoot, "v0.1.1", `${osPlatform}-${osArch}`);
  const bytes = Buffer.from("trusted mem-mcp binary");
  const requested = [];

  const installed = await install({
    osPlatform,
    osArch,
    version: "0.1.1",
    repository: "example/mem",
    environment: { MEM_MCP_CACHE_DIR: cacheRoot },
    homeDirectory: join(root, "read-only-package-home-must-not-be-used"),
    logger: QUIET_LOGGER,
    downloadText: async (url) => {
      requested.push(url);
      return manifestFor(bytes, asset);
    },
    downloadFile: async (url, destination) => {
      requested.push(url);
      writeFileSync(destination, bytes, { flag: "wx", mode: 0o600 });
    },
  });

  assert.equal(installed, join(cacheDir, asset));
  assert.deepEqual(readFileSync(installed), bytes);
  if (platform() !== "win32") {
    assert.notEqual(statSync(installed).mode & 0o111, 0);
  }
  assert.deepEqual(readdirSync(cacheDir), [asset]);
  assert.deepEqual(requested, [
    "https://github.com/example/mem/releases/download/v0.1.1/mem-mcp-checksums.txt",
    `https://github.com/example/mem/releases/download/v0.1.1/${asset}`,
  ]);
});

test("install verifies and reuses a cached binary", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const binPath = join(cacheDir, ASSET);
  const bytes = Buffer.from("cached verified binary");
  mkdirSync(cacheDir, { recursive: true });
  writeFileSync(binPath, bytes, { mode: 0o600 });
  chmodSync(binPath, 0o600);
  let binaryDownloads = 0;

  const installed = await install({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir,
    logger: QUIET_LOGGER,
    downloadText: async () => manifestFor(bytes),
    downloadFile: async () => {
      binaryDownloads += 1;
      throw new Error("verified cache must not be downloaded again");
    },
  });

  assert.equal(installed, binPath);
  assert.equal(binaryDownloads, 0);
  assert.deepEqual(readFileSync(binPath), bytes);
  if (platform() !== "win32") {
    assert.notEqual(statSync(binPath).mode & 0o111, 0);
  }
});

test("concurrent installers serialize and publish one verified binary", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const binPath = join(cacheDir, ASSET);
  const bytes = Buffer.from("one verified concurrent binary");
  let binaryDownloads = 0;

  const installs = Array.from({ length: 12 }, () => install({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir,
    logger: QUIET_LOGGER,
    lockPollMs: 1,
    downloadText: async () => manifestFor(bytes),
    downloadFile: async (_url, destination) => {
      binaryDownloads += 1;
      await new Promise((resolve) => setTimeout(resolve, 20));
      writeFileSync(destination, bytes, { flag: "wx", mode: 0o600 });
    },
  }));

  const installed = await Promise.all(installs);
  assert.deepEqual(installed, Array.from({ length: 12 }, () => binPath));
  assert.equal(binaryDownloads, 1);
  assert.deepEqual(readFileSync(binPath), bytes);
  assert.deepEqual(readdirSync(cacheDir), [ASSET]);
});

test("independent processes contend on one lock and download one binary", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const downloadLog = join(root, "binary-downloads.log");
  const hostAsset = assetFor(platform(), arch());
  const binPath = join(cacheDir, hostAsset);
  const bytes = Buffer.from("one verified binary shared across processes");
  const manifest = manifestFor(bytes, hostAsset);
  const installPath = require.resolve("./install");
  const workerProgram = `
    const { appendFileSync, writeFileSync } = require("node:fs");
    const { install } = require(${JSON.stringify(installPath)});
    const bytes = Buffer.from(${JSON.stringify(bytes.toString("base64"))}, "base64");
    install({
      osPlatform: ${JSON.stringify(platform())},
      osArch: ${JSON.stringify(arch())},
      cacheDir: ${JSON.stringify(cacheDir)},
      logger: { log() {}, warn() {} },
      lockPollMs: 2,
      downloadText: async () => ${JSON.stringify(manifest)},
      downloadFile: async (_url, destination) => {
        appendFileSync(${JSON.stringify(downloadLog)}, process.pid + "\\n");
        await new Promise((resolve) => setTimeout(resolve, 25));
        writeFileSync(destination, bytes, { flag: "wx", mode: 0o600 });
      },
    }).then(
      (installed) => {
        if (installed !== ${JSON.stringify(binPath)}) {
          throw new Error("unexpected install path: " + installed);
        }
      },
      (error) => {
        console.error(error.stack || error);
        process.exitCode = 1;
      },
    );
  `;

  await Promise.all(
    Array.from({ length: 12 }, () => runWorker(t, workerProgram)),
  );

  assert.deepEqual(readFileSync(binPath), bytes);
  assert.equal(readFileSync(downloadLog, "utf8").trim().split("\n").length, 1);
  assert.deepEqual(readdirSync(cacheDir), [hostAsset]);
});

test("stale lock recovery cleans its owner temp and preserves a foreign temp", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const lockPath = join(cacheDir, `.${ASSET}.lock`);
  const staleNonce = "a".repeat(24);
  const foreignNonce = "b".repeat(24);
  const staleTempName = `.${ASSET}.${staleNonce}.tmp`;
  const foreignTempName = `.${ASSET}.${foreignNonce}.tmp`;
  const bytes = Buffer.from("verified binary after stale lock recovery");
  mkdirSync(lockPath, { recursive: true });
  writeFileSync(
    join(lockPath, "owner.json"),
    `${JSON.stringify({
      pid: process.pid,
      hostname: `${hostname()}-stale-owner`,
      nonce: staleNonce,
    })}\n`,
  );
  writeFileSync(join(cacheDir, staleTempName), "stale owner partial");
  writeFileSync(join(cacheDir, foreignTempName), "foreign partial");

  assert.equal(await install({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir,
    logger: QUIET_LOGGER,
    lockPollMs: 1,
    lockStaleMs: 0,
    downloadText: async () => manifestFor(bytes),
    downloadFile: async (_url, destination) => {
      writeFileSync(destination, bytes, { flag: "wx", mode: 0o600 });
    },
  }), join(cacheDir, ASSET));

  assert.deepEqual(readFileSync(join(cacheDir, ASSET)), bytes);
  assert.deepEqual(readFileSync(join(cacheDir, foreignTempName), "utf8"), "foreign partial");
  assert.deepEqual(readdirSync(cacheDir).sort(), [ASSET, foreignTempName].sort());
});

test("a failed concurrent installer removes only its own temporary file", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const binPath = join(cacheDir, ASSET);
  const bytes = Buffer.from("verified winner after failed installer");
  let announceFailureDownload;
  let releaseFailureDownload;
  const failureDownloadStarted = new Promise((resolve) => {
    announceFailureDownload = resolve;
  });
  const allowFailure = new Promise((resolve) => {
    releaseFailureDownload = resolve;
  });

  const failed = install({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir,
    logger: QUIET_LOGGER,
    lockPollMs: 1,
    downloadText: async () => manifestFor(bytes),
    downloadFile: async (_url, destination) => {
      writeFileSync(destination, "partial", { flag: "wx", mode: 0o600 });
      announceFailureDownload();
      await allowFailure;
      throw new Error("simulated concurrent stream failure");
    },
  });
  await failureDownloadStarted;

  const winner = install({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir,
    logger: QUIET_LOGGER,
    lockPollMs: 1,
    downloadText: async () => manifestFor(bytes),
    downloadFile: async (_url, destination) => {
      writeFileSync(destination, bytes, { flag: "wx", mode: 0o600 });
    },
  });
  releaseFailureDownload();

  await assert.rejects(failed, /simulated concurrent stream failure/);
  assert.equal(await winner, binPath);
  assert.deepEqual(readFileSync(binPath), bytes);
  assert.deepEqual(readdirSync(cacheDir), [ASSET]);
});

test("install replaces a cached binary that fails verification", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const binPath = join(cacheDir, ASSET);
  const expected = Buffer.from("replacement verified binary");
  mkdirSync(cacheDir, { recursive: true });
  writeFileSync(binPath, "stale unverified binary", { mode: 0o755 });
  let binaryDownloads = 0;

  const installed = await install({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir,
    logger: QUIET_LOGGER,
    downloadText: async () => manifestFor(expected),
    downloadFile: async (_url, destination) => {
      binaryDownloads += 1;
      writeFileSync(destination, expected, { flag: "wx", mode: 0o600 });
    },
  });

  assert.equal(installed, binPath);
  assert.equal(binaryDownloads, 1);
  assert.deepEqual(readFileSync(binPath), expected);
  assert.deepEqual(readdirSync(cacheDir), [ASSET]);
});

test("failed replacement cleans only the quarantined invalid cache and own temp", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const binPath = join(cacheDir, ASSET);
  const expected = Buffer.from("expected replacement binary");
  mkdirSync(cacheDir, { recursive: true });
  writeFileSync(binPath, "invalid cached binary", { mode: 0o755 });

  await assert.rejects(
    install({
      osPlatform: "linux",
      osArch: "x64",
      cacheDir,
      logger: QUIET_LOGGER,
      downloadText: async () => manifestFor(expected),
      downloadFile: async (_url, destination) => {
        writeFileSync(destination, "partial", { flag: "wx", mode: 0o600 });
        throw new Error("replacement download failed");
      },
    }),
    /replacement download failed/,
  );

  assert.deepEqual(readdirSync(cacheDir), []);
});

test(
  "install replaces a broken cached symlink without following or deleting its target",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = testDirectory(t);
    const cacheDir = join(root, "cache");
    const binPath = join(cacheDir, ASSET);
    const missingTarget = join(root, "must-remain-missing");
    const expected = Buffer.from("verified replacement for broken symlink");
    mkdirSync(cacheDir, { recursive: true });
    symlinkSync(missingTarget, binPath);

    assert.equal(await install({
      osPlatform: "linux",
      osArch: "x64",
      cacheDir,
      logger: QUIET_LOGGER,
      downloadText: async () => manifestFor(expected),
      downloadFile: async (_url, destination) => {
        writeFileSync(destination, expected, { flag: "wx", mode: 0o600 });
      },
    }), binPath);

    assert.deepEqual(readFileSync(binPath), expected);
    assert.equal(readdirSync(root).includes("must-remain-missing"), false);
    assert.deepEqual(readdirSync(cacheDir), [ASSET]);
  },
);

test("checksum mismatch removes the temporary and final binary", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const expected = Buffer.from("expected binary");
  const substituted = Buffer.from("substituted binary");

  await assert.rejects(
    install({
      osPlatform: "linux",
      osArch: "x64",
      cacheDir,
      logger: QUIET_LOGGER,
      downloadText: async () => manifestFor(expected),
      downloadFile: async (_url, destination) => {
        writeFileSync(destination, substituted, { flag: "wx", mode: 0o600 });
      },
    }),
    /Checksum mismatch/,
  );

  assert.deepEqual(readdirSync(cacheDir), []);
});

test("partial download failure leaves no executable or temp file", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const expected = Buffer.from("expected binary");

  await assert.rejects(
    install({
      osPlatform: "linux",
      osArch: "x64",
      cacheDir,
      logger: QUIET_LOGGER,
      downloadText: async () => manifestFor(expected),
      downloadFile: async (_url, destination) => {
        writeFileSync(destination, "partial", { flag: "wx", mode: 0o600 });
        throw new Error("simulated HTTP stream reset");
      },
    }),
    /simulated HTTP stream reset/,
  );

  assert.deepEqual(readdirSync(cacheDir), []);
});

test("manifest failure removes an unverifiable executable from the service path", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const binPath = join(cacheDir, ASSET);
  mkdirSync(cacheDir, { recursive: true });
  writeFileSync(binPath, "unverified cache", { mode: 0o755 });

  await assert.rejects(
    install({
      osPlatform: "linux",
      osArch: "x64",
      cacheDir,
      logger: QUIET_LOGGER,
      downloadText: async () => {
        throw new Error("Download failed: HTTP 503");
      },
      downloadFile: async () => {
        throw new Error("binary download must not start");
      },
    }),
    /HTTP 503/,
  );

  assert.deepEqual(readdirSync(cacheDir), []);
});

test("manifest failure safely removes a directory occupying the service path", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const binPath = join(cacheDir, ASSET);
  mkdirSync(binPath, { recursive: true });
  writeFileSync(join(binPath, "not-a-binary"), "untrusted directory content");

  await assert.rejects(
    install({
      osPlatform: "linux",
      osArch: "x64",
      cacheDir,
      logger: QUIET_LOGGER,
      downloadText: async () => {
        throw new Error("invalid checksum manifest");
      },
    }),
    /invalid checksum manifest/,
  );

  assert.deepEqual(readdirSync(cacheDir), []);
});

test(
  "manifest failure unlinks a service-path symlink without following its target",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = testDirectory(t);
    const cacheDir = join(root, "cache");
    const binPath = join(cacheDir, ASSET);
    const target = join(root, "outside-target");
    mkdirSync(cacheDir, { recursive: true });
    writeFileSync(target, "must remain untouched");
    symlinkSync(target, binPath);

    await assert.rejects(
      install({
        osPlatform: "linux",
        osArch: "x64",
        cacheDir,
        logger: QUIET_LOGGER,
        downloadText: async () => {
          throw new Error("manifest unavailable");
        },
      }),
      /manifest unavailable/,
    );

    assert.deepEqual(readFileSync(target, "utf8"), "must remain untouched");
    assert.deepEqual(readdirSync(cacheDir), []);
  },
);

test("a logger failure after atomic publish preserves only the verified final", async (t) => {
  const root = testDirectory(t);
  const cacheDir = join(root, "cache");
  const binPath = join(cacheDir, ASSET);
  const bytes = Buffer.from("verified binary published before logger failure");
  const logger = {
    log(message) {
      if (message.includes("installed verified binary")) {
        throw new Error("diagnostic sink failed after publish");
      }
    },
    warn() {},
  };

  await assert.rejects(
    install({
      osPlatform: "linux",
      osArch: "x64",
      cacheDir,
      logger,
      downloadText: async () => manifestFor(bytes),
      downloadFile: async (_url, destination) => {
        writeFileSync(destination, bytes, { flag: "wx", mode: 0o600 });
      },
    }),
    /diagnostic sink failed after publish/,
  );

  assert.deepEqual(readFileSync(binPath), bytes);
  assert.deepEqual(readdirSync(cacheDir), [ASSET]);

  await assert.rejects(
    install({
      osPlatform: "linux",
      osArch: "x64",
      cacheDir,
      logger: {
        log(message) {
          if (message.includes("verified existing binary")) {
            throw new Error("diagnostic sink failed after cache verification");
          }
        },
        warn() {},
      },
      downloadText: async () => manifestFor(bytes),
      downloadFile: async () => {
        throw new Error("verified final must not be redownloaded");
      },
    }),
    /diagnostic sink failed after cache verification/,
  );

  assert.deepEqual(readFileSync(binPath), bytes);
  assert.deepEqual(readdirSync(cacheDir), [ASSET]);
});
