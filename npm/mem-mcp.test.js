"use strict";

const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const { spawn } = require("node:child_process");
const { mkdtempSync, readdirSync, rmSync } = require("node:fs");
const { tmpdir } = require("node:os");
const { join } = require("node:path");
const test = require("node:test");
const { assetFor } = require("./platforms");
const { installerLogger, run } = require("./mem-mcp");

const ASSET = assetFor("linux", "x64");
const CACHE_DIR = join("test-root", "cache");
const BIN_PATH = join(CACHE_DIR, ASSET);

function captureLogger() {
  const errors = [];
  return {
    errors,
    error(...args) {
      errors.push(args.join(" "));
    },
  };
}

function exitingChild(code, signal = null) {
  const child = new EventEmitter();
  queueMicrotask(() => child.emit("exit", code, signal));
  return child;
}

function waitForChildPid(runner, timeoutMs = 3000) {
  return new Promise((resolve, reject) => {
    let stdout = "";
    const timeout = setTimeout(() => {
      reject(new Error(`timed out waiting for child pid; stdout=${stdout}`));
    }, timeoutMs);
    runner.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
      const match = /CHILD_PID=(\d+)/.exec(stdout);
      if (match) {
        clearTimeout(timeout);
        resolve(Number(match[1]));
      }
    });
    runner.once("exit", (code, signal) => {
      clearTimeout(timeout);
      reject(new Error(`runner exited before child pid: code=${code} signal=${signal}`));
    });
  });
}

function waitForOutput(runner, expected, timeoutMs = 3000) {
  return new Promise((resolve, reject) => {
    let stdout = "";
    const timeout = setTimeout(() => {
      reject(new Error(`timed out waiting for ${expected}; stdout=${stdout}`));
    }, timeoutMs);
    runner.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
      if (stdout.includes(expected)) {
        clearTimeout(timeout);
        resolve(stdout);
      }
    });
    runner.once("exit", (code, signal) => {
      clearTimeout(timeout);
      reject(new Error(`runner exited before ${expected}: code=${code} signal=${signal}`));
    });
  });
}

function waitForExit(child, timeoutMs = 3000) {
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      reject(new Error(`timed out waiting for process ${child.pid} to exit`));
    }, timeoutMs);
    child.once("exit", (code, signal) => {
      clearTimeout(timeout);
      resolve({ code, signal });
    });
  });
}

function collectOutput(stream) {
  let output = "";
  stream.on("data", (chunk) => {
    output += chunk.toString();
  });
  return () => output;
}

function processExists(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    if (err.code === "ESRCH") return false;
    throw err;
  }
}

async function assertProcessGone(pid, timeoutMs = 1000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (!processExists(pid)) return;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  assert.equal(processExists(pid), false, `process ${pid} is still alive`);
}

async function runSignalScenario(t, childSignalHandler) {
  const wrapperPath = require.resolve("./mem-mcp");
  const childProgram = [
    `process.on("SIGTERM", ${childSignalHandler});`,
    'console.log("CHILD_PID=" + process.pid);',
    "setInterval(() => {}, 1000);",
  ].join("\n");
  const runnerProgram = `
    const { runProcess } = require(${JSON.stringify(wrapperPath)});
    runProcess({
      osPlatform: "linux",
      osArch: "x64",
      install: async () => process.execPath,
      args: ["-e", ${JSON.stringify(childProgram)}],
      signalGraceMs: 100,
    }).then(
      (result) => { process.exitCode = result.code; },
      (error) => { console.error(error); process.exitCode = 97; },
    );
  `;
  const runner = spawn(process.execPath, ["-e", runnerProgram], {
    stdio: ["ignore", "pipe", "pipe"],
  });
  let childPid = null;
  t.after(() => {
    if (runner.exitCode === null && runner.signalCode === null) {
      runner.kill("SIGKILL");
    }
    if (childPid !== null && processExists(childPid)) {
      process.kill(childPid, "SIGKILL");
    }
  });

  childPid = await waitForChildPid(runner);
  const exited = waitForExit(runner);
  assert.equal(runner.kill("SIGTERM"), true);
  const result = await exited;
  await assertProcessGone(childPid);
  return { childPid, result };
}

test("missing binary bootstraps with stderr-only logging before spawn", async () => {
  const logger = captureLogger();
  const calls = [];

  const code = await run({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir: CACHE_DIR,
    args: ["--server", "http://127.0.0.1:8787"],
    env: { MEM_TOKEN: "test-token" },
    logger,
    install: async (options) => {
      calls.push([
        "install",
        options.osPlatform,
        options.osArch,
        options.cacheDir,
        options.environment.MEM_TOKEN,
      ]);
      options.logger.log("manifest download");
      options.logger.warn("cache warning");
      return BIN_PATH;
    },
    spawn: (path, args, options) => {
      calls.push(["spawn", path, args, options]);
      return exitingChild(0);
    },
  });

  assert.equal(code, 0);
  assert.deepEqual(calls, [
    ["install", "linux", "x64", CACHE_DIR, "test-token"],
    [
      "spawn",
      BIN_PATH,
      ["--server", "http://127.0.0.1:8787"],
      { env: { MEM_TOKEN: "test-token" }, stdio: "inherit" },
    ],
  ]);
  assert.deepEqual(logger.errors, ["manifest download", "cache warning"]);
});

test("present binary is reverified while the installer reuses its cache", async () => {
  let verifications = 0;
  let spawned = null;

  const code = await run({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir: CACHE_DIR,
    install: async (options) => {
      verifications += 1;
      assert.equal(options.cacheDir, CACHE_DIR);
      return BIN_PATH;
    },
    spawn: (path) => {
      spawned = path;
      return exitingChild(0);
    },
  });

  assert.equal(code, 0);
  assert.equal(verifications, 1);
  assert.equal(spawned, BIN_PATH);
});

test("bootstrap failure is actionable and never starts a child", async () => {
  const logger = captureLogger();
  let spawns = 0;

  const code = await run({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir: CACHE_DIR,
    logger,
    install: async () => {
      throw new Error("checksum mismatch");
    },
    spawn: () => {
      spawns += 1;
      return exitingChild(0);
    },
  });

  assert.equal(code, 1);
  assert.equal(spawns, 0);
  assert.match(logger.errors[0], /failed to download and verify/);
  assert.match(logger.errors[0], /checksum mismatch/);
  assert.match(logger.errors[1], /retry the command/);
});

test("arguments, environment, inherited stdio, and child exit code propagate", async () => {
  const args = ["--workspace", "workspace-id", "--help"];
  const env = { MEM_SERVER: "http://127.0.0.1:8787" };
  let invocation = null;

  const code = await run({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir: CACHE_DIR,
    args,
    env,
    install: async () => BIN_PATH,
    spawn: (path, childArgs, options) => {
      invocation = { path, childArgs, options };
      return exitingChild(23);
    },
  });

  assert.equal(code, 23);
  assert.deepEqual(invocation, {
    path: BIN_PATH,
    childArgs: args,
    options: { env, stdio: "inherit" },
  });
});

test("child start errors and signal exits remain nonzero", async () => {
  const logger = captureLogger();
  const failedChild = new EventEmitter();

  const startFailure = run({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir: CACHE_DIR,
    logger,
    install: async () => BIN_PATH,
    spawn: () => {
      queueMicrotask(() => {
        failedChild.emit("error", new Error("EACCES"));
        failedChild.emit("exit", 1, null);
      });
      return failedChild;
    },
  });

  assert.equal(await startFailure, 1);
  assert.equal(logger.errors.length, 1);
  assert.match(logger.errors[0], /EACCES/);

  const signalExit = await run({
    osPlatform: "linux",
    osArch: "x64",
    cacheDir: CACHE_DIR,
    install: async () => BIN_PATH,
    spawn: () => exitingChild(null, "SIGTERM"),
  });
  assert.equal(signalExit, 1);
});

test("a real child receives stdin EOF and owns clean stdout", async (t) => {
  const wrapperPath = require.resolve("./mem-mcp");
  const childProgram = [
    'process.stdin.once("end", () => console.log("CHILD_STDIN_EOF"));',
    "process.stdin.resume();",
  ].join("\n");
  const runnerProgram = `
    const { runProcess } = require(${JSON.stringify(wrapperPath)});
    runProcess({
      osPlatform: process.platform,
      osArch: process.arch,
      install: async (options) => {
        options.logger.log("BOOTSTRAP_DIAGNOSTIC");
        return process.execPath;
      },
      args: ["-e", ${JSON.stringify(childProgram)}],
    }).then(
      (result) => { process.exitCode = result.code; },
      (error) => { console.error(error); process.exitCode = 97; },
    );
  `;
  const runner = spawn(process.execPath, ["-e", runnerProgram], {
    stdio: ["pipe", "pipe", "pipe"],
  });
  const stdout = collectOutput(runner.stdout);
  const stderr = collectOutput(runner.stderr);
  t.after(() => {
    if (runner.exitCode === null && runner.signalCode === null) {
      runner.kill("SIGKILL");
    }
  });

  const exited = waitForExit(runner);
  runner.stdin.end();
  assert.deepEqual(await exited, { code: 0, signal: null });
  assert.equal(stdout(), "CHILD_STDIN_EOF\n");
  assert.equal(stderr(), "BOOTSTRAP_DIAGNOSTIC\n");
});

test(
  "parent SIGTERM is forwarded to the real child without leaving an orphan",
  { skip: process.platform === "win32" },
  async (t) => {
    const { result } = await runSignalScenario(t, "() => process.exit(0)");
    assert.deepEqual(result, { code: 1, signal: null });
  },
);

test(
  "a real child that ignores SIGTERM is killed after the bounded grace period",
  { skip: process.platform === "win32" },
  async (t) => {
    const { result } = await runSignalScenario(t, "() => {}");
    assert.deepEqual(result, { code: 1, signal: null });
  },
);

test(
  "SIGTERM during real bootstrap aborts the download and cleans its temp and lock",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = mkdtempSync(join(tmpdir(), "mem-mcp-bootstrap-signal-test-"));
    const cacheDir = join(root, "cache");
    const wrapperPath = require.resolve("./mem-mcp");
    const installPath = require.resolve("./install");
    const runnerProgram = `
      const { createHash } = require("node:crypto");
      const { writeFileSync } = require("node:fs");
      const { install } = require(${JSON.stringify(installPath)});
      const { runProcess } = require(${JSON.stringify(wrapperPath)});
      const bytes = Buffer.from("verified binary that must never publish");
      const asset = "mem-mcp-linux-amd64";
      const digest = createHash("sha256").update(bytes).digest("hex");
      const manifest = digest + "  " + asset + "\\n";
      runProcess({
        osPlatform: "linux",
        osArch: "x64",
        install: (options) => install({
          ...options,
          cacheDir: ${JSON.stringify(cacheDir)},
          lockPollMs: 1,
          downloadText: async () => manifest,
          downloadFile: async (_url, destination, requestOptions) => {
            writeFileSync(destination, "partial", { flag: "wx", mode: 0o600 });
            console.log("DOWNLOAD_STARTED");
            await new Promise((resolve, reject) => {
              const signal = requestOptions.signal;
              const holdOpen = setInterval(() => {}, 1000);
              const abort = () => {
                clearInterval(holdOpen);
                reject(signal.reason || new Error("aborted"));
              };
              signal.addEventListener("abort", abort, { once: true });
              if (signal.aborted) abort();
            });
          },
        }),
      }).then(
        (result) => { process.exitCode = result.code; },
        (error) => { console.error(error); process.exitCode = 97; },
      );
    `;
    const runner = spawn(process.execPath, ["-e", runnerProgram], {
      stdio: ["ignore", "pipe", "pipe"],
    });
    t.after(() => {
      if (runner.exitCode === null && runner.signalCode === null) {
        runner.kill("SIGKILL");
      }
      rmSync(root, { recursive: true, force: true });
    });

    await waitForOutput(runner, "DOWNLOAD_STARTED");
    const exited = waitForExit(runner);
    assert.equal(runner.kill("SIGTERM"), true);
    assert.deepEqual(await exited, { code: 1, signal: null });
    assert.deepEqual(readdirSync(cacheDir), []);
  },
);

test("unsupported platforms fail before bootstrap or spawn", async () => {
  const logger = captureLogger();
  let sideEffects = 0;

  const code = await run({
    osPlatform: "freebsd",
    osArch: "riscv64",
    logger,
    install: async () => {
      sideEffects += 1;
    },
    spawn: () => {
      sideEffects += 1;
      return exitingChild(0);
    },
  });

  assert.equal(code, 1);
  assert.equal(sideEffects, 0);
  assert.match(logger.errors[0], /Unsupported platform: freebsd-riscv64/);
});

test("installer log and warning channels are mapped only to stderr", () => {
  const calls = [];
  const logger = {
    error(...args) {
      calls.push(["error", ...args]);
    },
    log(...args) {
      calls.push(["stdout", ...args]);
    },
  };
  const mapped = installerLogger(logger);

  mapped.log("download");
  mapped.warn("warning");

  assert.deepEqual(calls, [
    ["error", "download"],
    ["error", "warning"],
  ]);
});
