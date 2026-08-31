"use strict";

const assert = require("node:assert/strict");
const { spawnSync } = require("node:child_process");
const { createHash } = require("node:crypto");
const {
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
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

test(
  "the installed Windows npm .cmd shim runs a verified cached executable",
  { skip: platform() !== "win32" },
  () => {
    const npmCli = process.env.npm_execpath;
    assert.ok(npmCli, "npm_execpath must identify the npm CLI under test");

    const root = mkdtempSync(join(tmpdir(), "mem-mcp-windows-shim-test-"));
    try {
      const consumer = join(root, "consumer");
      const npmCache = join(root, "npm-cache");
      const runtimeCacheRoot = join(root, "runtime-cache");
      const userConfig = join(root, "empty-npmrc");
      const requestLog = join(root, "https-requests.log");
      const hookPath = join(root, "release-double.cjs");
      const childPath = join(root, "child-stdout.js");
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
      const packResults = JSON.parse(packed.stdout);
      assert.equal(packResults.length, 1, packed.stdout);
      const tarball = resolve(root, packResults[0].filename);

      run(
        process.execPath,
        [
          npmCli,
          "install",
          "--offline",
          "--ignore-scripts",
          "--no-audit",
          "--no-fund",
          "--package-lock=false",
          `--cache=${npmCache}`,
          `--userconfig=${userConfig}`,
          tarball,
        ],
        { cwd: consumer },
      );

      const asset = assetFor(platform(), arch());
      const runtimeDir = join(
        runtimeCacheRoot,
        `v${PACKAGE_VERSION}`,
        `${platform()}-${arch()}`,
      );
      const binaryPath = join(runtimeDir, asset);
      mkdirSync(runtimeDir, { recursive: true });
      copyFileSync(process.execPath, binaryPath);
      const digest = createHash("sha256")
        .update(readFileSync(binaryPath))
        .digest("hex");
      const manifest = `${digest}  ${asset}\n`;
      const hook = `
"use strict";
const { appendFileSync } = require("node:fs");
const { EventEmitter } = require("node:events");
const { PassThrough } = require("node:stream");
const https = require("node:https");
const manifest = Buffer.from(${JSON.stringify(manifest)});
const requestLog = ${JSON.stringify(requestLog)};
https.get = (url, _options, callback) => {
  const request = new EventEmitter();
  const target = url instanceof URL ? url : new URL(url);
  queueMicrotask(() => {
    appendFileSync(requestLog, target.href + "\\n");
    if (!target.pathname.endsWith("/mem-mcp-checksums.txt")) {
      request.emit("error", new Error("unexpected binary download: " + target.href));
      return;
    }
    const response = new PassThrough();
    response.statusCode = 200;
    response.headers = { "content-length": String(manifest.length) };
    callback(response);
    response.end(manifest);
  });
  return request;
};
`;
      writeFileSync(hookPath, hook);
      writeFileSync(childPath, 'process.stdout.write("WINDOWS_CMD_SHIM_OK\\n");\n');

      const wrapper = join(consumer, "node_modules", ".bin", "mem-mcp.cmd");
      assert.equal(existsSync(wrapper), true, "npm must generate the Windows .cmd shim");
      const commandLine = `"${wrapper}" "${childPath}"`;
      const invocation = spawnSync(commandLine, {
        cwd: consumer,
        encoding: "utf8",
        env: {
          ...process.env,
          MEM_MCP_CACHE_DIR: runtimeCacheRoot,
          NODE_OPTIONS: `--require=${hookPath}`,
        },
        shell: process.env.ComSpec || true,
      });

      assert.equal(invocation.status, 0, invocation.stderr);
      assert.equal(invocation.stdout, "WINDOWS_CMD_SHIM_OK\n");
      assert.match(invocation.stderr, /downloading .*mem-mcp-checksums\.txt/);
      assert.match(invocation.stderr, /verified existing binary/);
      assert.deepEqual(
        readFileSync(requestLog, "utf8").trim().split("\n"),
        [`https://github.com/fullstack-ai-infra/mem/releases/download/v${PACKAGE_VERSION}/mem-mcp-checksums.txt`],
      );
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  },
);
