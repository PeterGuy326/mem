"use strict";

const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const { createHash } = require("node:crypto");
const {
  chmodSync,
  existsSync,
  lstatSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} = require("node:fs");
const { arch, platform, tmpdir } = require("node:os");
const { join, resolve } = require("node:path");
const test = require("node:test");
const { assetFor } = require("./platforms");
const { version: PACKAGE_VERSION } = require("./package.json");

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    ...options,
    encoding: "utf8",
  });
  assert.equal(
    result.status,
    0,
    [
      `${command} ${args.join(" ")} exited ${result.status}`,
      result.stdout,
      result.stderr,
    ].join("\n"),
  );
  return result;
}

function makeTreeReadOnly(root, originalModes = new Map()) {
  const info = lstatSync(root);
  if (info.isSymbolicLink()) return originalModes;
  originalModes.set(root, info.mode & 0o777);
  if (info.isDirectory()) {
    for (const entry of readdirSync(root)) {
      makeTreeReadOnly(join(root, entry), originalModes);
    }
  }
  chmodSync(root, (info.mode & 0o777) & ~0o222);
  return originalModes;
}

function restoreTreeModes(originalModes) {
  for (const [target, mode] of originalModes) {
    if (existsSync(target)) chmodSync(target, mode);
  }
}

test(
  "npm 12 clean tarball lazily bootstraps a verified binary without scripts",
  { skip: platform() !== "linux" },
  () => {
    const npmCli = process.env.npm_execpath;
    assert.ok(npmCli, "npm_execpath must identify the npm 12 CLI under test");

    const npmVersion = run(process.execPath, [npmCli, "--version"]).stdout.trim();
    assert.match(npmVersion, /^12\./, `expected npm 12, got ${npmVersion}`);

    const root = mkdtempSync(join(tmpdir(), "mem-mcp-npm12-test-"));
    let originalPackageModes = null;
    try {
      const consumer = join(root, "consumer");
      const cache = join(root, "npm-cache");
      const runtimeCacheRoot = join(root, "runtime-cache");
      const userConfig = join(root, "empty-npmrc");
      const requestLog = join(root, "https-requests.log");
      const hookPath = join(root, "release-double.cjs");
      mkdirSync(consumer);
      writeFileSync(join(consumer, "package.json"), '{"private":true}\n');
      writeFileSync(userConfig, "");

      const packed = run(
        process.execPath,
        [
          npmCli,
          "pack",
          "--ignore-scripts",
          "--json",
          "--pack-destination",
          root,
        ],
        { cwd: __dirname },
      );
      const parsedPackResults = JSON.parse(packed.stdout);
      const packResults = Array.isArray(parsedPackResults)
        ? parsedPackResults
        : Object.values(parsedPackResults);
      assert.equal(
        packResults.length,
        1,
        `expected one packed artifact, got: ${packed.stdout}`,
      );
      const packResult = packResults[0];
      const tarball = resolve(root, packResult.filename);
      assert.deepEqual(
        packResult.files.map((file) => file.path).sort(),
        [
          "LICENSE",
          "README.md",
          "install.js",
          "mem-mcp",
          "package.json",
          "platforms.js",
        ],
      );

      run(
        process.execPath,
        [
          npmCli,
          "install",
          "--offline",
          "--no-audit",
          "--no-fund",
          "--package-lock=false",
          `--cache=${cache}`,
          `--userconfig=${userConfig}`,
          tarball,
        ],
        { cwd: consumer },
      );

      const packageRoot = join(
        consumer,
        "node_modules",
        "@fullstack-ai-infra",
        "mem-mcp",
      );
      const packageJson = JSON.parse(
        readFileSync(join(packageRoot, "package.json"), "utf8"),
      );
      assert.equal(packageJson.version, PACKAGE_VERSION);
      assert.equal(packageJson.scripts.postinstall, undefined);

      const asset = assetFor(platform(), arch());
      const packageBinaryPath = join(packageRoot, "bin", asset);
      assert.equal(
        existsSync(packageBinaryPath),
        false,
        "installing the tarball must not execute a dependency lifecycle script",
      );
      const binaryPath = join(
        runtimeCacheRoot,
        `v${PACKAGE_VERSION}`,
        `${platform()}-${arch()}`,
        asset,
      );

      originalPackageModes = new Map();
      makeTreeReadOnly(packageRoot, originalPackageModes);

      const binary = Buffer.from(
        "#!/bin/sh\nprintf 'fixture stdout:%s:%s\\n' \"$MEM_TARBALL_SENTINEL\" \"$*\"\n",
      );
      const digest = createHash("sha256").update(binary).digest("hex");
      const manifest = `${digest}  ${asset}\n`;
      const hook = `
"use strict";
const { appendFileSync } = require("node:fs");
const { EventEmitter } = require("node:events");
const { PassThrough } = require("node:stream");
const https = require("node:https");
const asset = ${JSON.stringify(asset)};
const binary = Buffer.from(${JSON.stringify(binary.toString("base64"))}, "base64");
const manifest = Buffer.from(${JSON.stringify(manifest)});
const requestLog = ${JSON.stringify(requestLog)};
https.get = (url, _options, callback) => {
  const request = new EventEmitter();
  const target = url instanceof URL ? url : new URL(url);
  queueMicrotask(() => {
    appendFileSync(requestLog, target.href + "\\n");
    let body;
    if (target.pathname.endsWith("/mem-mcp-checksums.txt")) {
      body = manifest;
    } else if (target.pathname.endsWith("/" + asset)) {
      body = binary;
    } else {
      request.emit("error", new Error("unexpected release URL: " + target.href));
      return;
    }
    const response = new PassThrough();
    response.statusCode = 200;
    response.headers = { "content-length": String(body.length) };
    callback(response);
    response.end(body);
  });
  return request;
};
`;
      writeFileSync(hookPath, hook);

      const wrapper = join(consumer, "node_modules", ".bin", "mem-mcp");
      const wrapperEnv = {
        ...process.env,
        MEM_MCP_CACHE_DIR: runtimeCacheRoot,
        MEM_TARBALL_SENTINEL: "inherited-env",
        NODE_OPTIONS: `--require=${hookPath}`,
      };
      const first = spawnSync(wrapper, ["--probe", "first-run"], {
        cwd: consumer,
        encoding: "utf8",
        env: wrapperEnv,
      });

      assert.equal(first.status, 0, first.stderr);
      assert.equal(
        first.stdout,
        "fixture stdout:inherited-env:--probe first-run\n",
      );
      assert.match(first.stderr, /downloading .*mem-mcp-checksums\.txt/);
      assert.match(first.stderr, new RegExp(`downloading .*${asset}`));
      assert.match(first.stderr, /installed verified binary/);
      assert.deepEqual(readFileSync(binaryPath), binary);
      assert.notEqual(statSync(binaryPath).mode & 0o111, 0);
      assert.equal(
        existsSync(packageBinaryPath),
        false,
        "first invocation must not write into the read-only installed package",
      );

      const firstRequests = readFileSync(requestLog, "utf8").trim().split("\n");
      assert.equal(firstRequests.length, 2);
      assert.ok(
        firstRequests[0].endsWith(
          `/releases/download/v${PACKAGE_VERSION}/mem-mcp-checksums.txt`,
        ),
      );
      assert.ok(
        firstRequests[1].endsWith(
          `/releases/download/v${PACKAGE_VERSION}/${asset}`,
        ),
      );

      const second = spawnSync(wrapper, ["--probe", "cached-run"], {
        cwd: consumer,
        encoding: "utf8",
        env: wrapperEnv,
      });
      assert.equal(second.status, 0, second.stderr);
      assert.equal(
        second.stdout,
        "fixture stdout:inherited-env:--probe cached-run\n",
      );
      assert.match(second.stderr, /downloading .*mem-mcp-checksums\.txt/);
      assert.match(second.stderr, /verified existing binary/);
      const secondRequests = readFileSync(requestLog, "utf8").trim().split("\n");
      assert.deepEqual(secondRequests.slice(0, 2), firstRequests);
      assert.equal(secondRequests.length, 3);
      assert.ok(
        secondRequests[2].endsWith(
          `/releases/download/v${PACKAGE_VERSION}/mem-mcp-checksums.txt`,
        ),
      );
      assert.equal(
        secondRequests.filter((url) => url.endsWith(`/${asset}`)).length,
        1,
        "cached invocation must reverify the manifest without redownloading the binary",
      );

      writeFileSync(
        binaryPath,
        "#!/bin/sh\nprintf 'tampered cache must never run\\n'\n",
      );
      const repaired = spawnSync(wrapper, ["--probe", "repaired-cache"], {
        cwd: consumer,
        encoding: "utf8",
        env: wrapperEnv,
      });
      assert.equal(repaired.status, 0, repaired.stderr);
      assert.equal(
        repaired.stdout,
        "fixture stdout:inherited-env:--probe repaired-cache\n",
      );
      assert.doesNotMatch(repaired.stdout, /tampered cache/);
      assert.match(repaired.stderr, /removed unverified cached binary/);
      assert.match(repaired.stderr, new RegExp(`downloading .*${asset}`));
      assert.deepEqual(readFileSync(binaryPath), binary);
      assert.equal(existsSync(packageBinaryPath), false);
      const repairedRequests = readFileSync(requestLog, "utf8").trim().split("\n");
      assert.equal(repairedRequests.length, 5);
      assert.equal(
        repairedRequests.filter((url) => url.endsWith(`/${asset}`)).length,
        2,
        "a substituted cache must be replaced before the child starts",
      );
    } finally {
      if (originalPackageModes !== null) restoreTreeModes(originalPackageModes);
      rmSync(root, { recursive: true, force: true });
    }
  },
);
