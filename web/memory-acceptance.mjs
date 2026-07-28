import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const webRoot = path.dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.MEMORY_E2E_PORT ?? 5192);
const baseURL = `http://127.0.0.1:${port}`;
const artifactDir = path.join(tmpdir(), `mem-memory-acceptance-${process.pid}`);
const decisionID = '30000000-0000-4000-8000-000000000001';
const preferenceID = '30000000-0000-4000-8000-000000000002';
const artifactID = '30000000-0000-4000-8000-000000000003';
const observationID = '30000000-0000-4000-8000-000000000004';
const sourceFileID = 'hero-2012-yunnan-xiaoming';

function startVite() {
  const vite = path.join(webRoot, 'node_modules', 'vite', 'bin', 'vite.js');
  const child = spawn(
    process.execPath,
    [vite, '--host', '127.0.0.1', '--port', String(port), '--strictPort'],
    {
      cwd: webRoot,
      env: {
        ...process.env,
        VITE_USE_MOCK: 'true',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  );
  let output = '';
  child.stdout.on('data', (chunk) => {
    output += chunk.toString();
  });
  child.stderr.on('data', (chunk) => {
    output += chunk.toString();
  });
  return { child, output: () => output };
}

async function waitForServer(server) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (server.child.exitCode !== null) {
      throw new Error(`Vite exited before startup.\n${server.output()}`);
    }
    try {
      const response = await fetch(baseURL);
      if (response.ok) return;
    } catch {
      // The development server is still starting.
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`Timed out waiting for ${baseURL}.\n${server.output()}`);
}

async function stopProcess(child) {
  if (child.exitCode !== null) return;
  child.kill('SIGTERM');
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 2_000)),
  ]);
  if (child.exitCode === null) child.kill('SIGKILL');
}

async function closeBrowserWithin(browser, timeoutMs = 5_000) {
  if (!browser) return;
  let timeout;
  try {
    const closed = await Promise.race([
      browser.close().then(() => true),
      new Promise((resolve) => {
        timeout = setTimeout(() => resolve(false), timeoutMs);
      }),
    ]);
    if (!closed) {
      console.warn(`Browser close exceeded ${timeoutMs}ms; forcing runner exit after Vite cleanup.`);
    }
  } finally {
    clearTimeout(timeout);
  }
}

async function launchBrowser() {
  try {
    return await chromium.launch({ headless: true });
  } catch (error) {
    if (!(error instanceof Error) || !error.message.includes("Executable doesn't exist")) {
      throw error;
    }
    // Local contributors often have Chrome but not Playwright's optional
    // browser download. CI can continue using the bundled Chromium above.
    return chromium.launch({ channel: 'chrome', headless: true });
  }
}

async function addMockSession(context, token, email = 'memory-e2e@mem.dev') {
  await context.addInitScript(
    ({ sessionToken, sessionEmail }) => {
      localStorage.setItem('mem.token', sessionToken);
      localStorage.setItem(
        'mem.user',
        JSON.stringify({
          id: 'user-1',
          email: sessionEmail,
          created_at: '2026-01-01T00:00:00Z',
        }),
      );
      localStorage.setItem('mem.lang', 'en');
    },
    { sessionToken: token, sessionEmail: email },
  );
}

async function readJSON(page, url, options) {
  return page.evaluate(
    async ({ requestURL, requestOptions }) => {
      const response = await fetch(requestURL, requestOptions);
      return {
        status: response.status,
        body: await response.json(),
      };
    },
    { requestURL: url, requestOptions: options },
  );
}

const vite = startVite();
let browser;
let failure;
let failed = false;

try {
  await waitForServer(vite);
  await mkdir(artifactDir, { recursive: true });

  browser = await launchBrowser();
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    permissions: ['clipboard-read', 'clipboard-write'],
  });
  await context.addInitScript(() => {
    localStorage.setItem('mem.token', 'mock-e2e-token');
    localStorage.setItem(
      'mem.user',
      JSON.stringify({
        id: 'user-1',
        email: 'memory-e2e@mem.dev',
        created_at: '2026-01-01T00:00:00Z',
      }),
    );
    localStorage.setItem('mem.lang', 'en');
  });

  const page = await context.newPage();
  page.setDefaultTimeout(10_000);
  page.setDefaultNavigationTimeout(15_000);
  const pageErrors = [];
  const consoleErrors = [];
  const unexpectedDialogs = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('dialog', async (dialog) => {
    unexpectedDialogs.push(dialog.message());
    await dialog.dismiss();
  });

  await page.goto(`${baseURL}/memories`, { waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: 'Memory ledger' }).waitFor();
  await page.getByTestId(`memory-${decisionID}`).waitFor();
  console.log('✓ memory ledger loaded');

  const listContract = await readJSON(page, '/v1/memories?lifecycle=all&limit=100');
  assert.equal(listContract.status, 200);
  assert.ok(Array.isArray(listContract.body.memories));
  assert.ok(listContract.body.memories.length >= 6);
  for (const memory of listContract.body.memories) {
    assert.equal('content' in memory, false, 'list response must not expose full content');
    assert.ok(
      Array.from(memory.excerpt).length <= 500,
      'list excerpt must be bounded to 500 Unicode code points',
    );
    assert.equal(typeof memory.content_length, 'number');
  }
  console.log('✓ bounded summary contract');

  const firstPage = await readJSON(page, '/v1/memories?lifecycle=all&limit=2');
  assert.equal(firstPage.status, 200);
  assert.equal(firstPage.body.memories.length, 2);
  assert.equal(typeof firstPage.body.next_cursor, 'string');
  const secondPage = await readJSON(
    page,
    `/v1/memories?lifecycle=all&limit=2&cursor=${encodeURIComponent(firstPage.body.next_cursor)}`,
  );
  assert.equal(secondPage.status, 200);
  assert.equal(secondPage.body.memories.length, 2);
  assert.equal(
    secondPage.body.memories.some((memory) =>
      firstPage.body.memories.some((previous) => previous.id === memory.id),
    ),
    false,
    'opaque keyset pages must not overlap',
  );
  const mismatchedCursor = await readJSON(
    page,
    `/v1/memories?scope=%2FOther&lifecycle=all&limit=2&cursor=${encodeURIComponent(firstPage.body.next_cursor)}`,
  );
  assert.equal(mismatchedCursor.status, 400);
  assert.equal(mismatchedCursor.body.error, 'invalid_memory_query');
  console.log('✓ opaque authorization/filter-bound cursor');

  const idempotencyKeys = await page.evaluate(async () => {
    const { memoryActionKey } = await import('/src/lib/memory-idempotency.ts');
    return {
      actorOne: memoryActionKey('user-1', 'memory-1', 7, 'feedback-useful'),
      actorOneRetry: memoryActionKey('user-1', 'memory-1', 7, 'feedback-useful'),
      actorTwo: memoryActionKey('user-2', 'memory-1', 7, 'feedback-useful'),
    };
  });
  assert.equal(
    idempotencyKeys.actorOne,
    idempotencyKeys.actorOneRetry,
    'the same actor retry must reuse the same key',
  );
  assert.notEqual(
    idempotencyKeys.actorOne,
    idempotencyKeys.actorTwo,
    'different actors must never share an idempotency key',
  );
  console.log('✓ actor-scoped idempotency keys');

  const idempotentTarget = await readJSON(page, `/v1/memories/${observationID}`);
  const replayOptions = {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': `memory-e2e-replay-${observationID}`,
    },
    body: JSON.stringify({
      action: 'useful',
      expected_version: idempotentTarget.body.state_version,
    }),
  };
  const firstMutation = await readJSON(
    page,
    `/v1/memories/${observationID}/feedback`,
    replayOptions,
  );
  const replayedMutation = await readJSON(
    page,
    `/v1/memories/${observationID}/feedback`,
    replayOptions,
  );
  assert.equal(firstMutation.status, 201);
  assert.equal(firstMutation.body.replayed, false);
  assert.equal(replayedMutation.status, 200);
  assert.equal(replayedMutation.body.replayed, true);
  assert.equal(replayedMutation.body.event.id, firstMutation.body.event.id);
  assert.equal(
    replayedMutation.body.event.resulting_version,
    firstMutation.body.event.resulting_version,
  );
  console.log('✓ mutation idempotent replay contract');

  await page.getByTestId(`memory-${decisionID}`).click();
  await page
    .getByLabel('Memory detail')
    .getByText('mem 只负责返回带来源的 Context Pack；最终推理与回答始终由外部 Agent 完成。', {
      exact: true,
    })
    .waitFor();
  await page.screenshot({
    path: path.join(artifactDir, 'memory-ledger-detail.png'),
    fullPage: true,
  });

  const fullDetail = await readJSON(page, `/v1/memories/${decisionID}`);
  assert.equal(fullDetail.status, 200);
  assert.equal(typeof fullDetail.body.content, 'string');
  assert.equal(fullDetail.body.citation, `mem://memories/${decisionID}`);
  assert.equal(fullDetail.body.provenance.workspace_id, fullDetail.body.workspace_id);
  assert.equal(fullDetail.body.provenance.source_type, fullDetail.body.source_type);
  console.log('✓ detail and citation');

  await page.getByRole('button', { name: 'Unpin', exact: true }).click();
  await page.getByRole('button', { name: 'Pin', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Pin', exact: true }).click();
  await page.getByRole('button', { name: 'Unpin', exact: true }).waitFor();
  await page.getByRole('button', { name: /Useful · 3/ }).click();
  await page.getByRole('button', { name: /Useful · 4/ }).waitFor();

  await page.getByRole('button', { name: 'Archive', exact: true }).click();
  await page.getByRole('button', { name: 'Restore', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Restore', exact: true }).click();
  await page.getByRole('button', { name: 'Archive', exact: true }).waitFor();
  console.log('✓ pin, feedback, archive, and restore controls');

  await page.goto(`${baseURL}/memories/${observationID}`, {
    waitUntil: 'domcontentloaded',
  });
  await page
    .locator('pre')
    .filter({ hasText: '<script>alert("never execute memory content")</script>' })
    .waitFor();
  assert.deepEqual(unexpectedDialogs, [], 'untrusted memory content must remain inert text');
  console.log('✓ untrusted content remains plain text');

  await page.goto(`${baseURL}/memories/${preferenceID}`, {
    waitUntil: 'domcontentloaded',
  });
  await page
    .getByLabel('Memory detail')
    .getByText('用户偏好：交付说明先给结论，再列验证证据；避免把内部工具名当作产品价值。', {
      exact: true,
    })
    .waitFor();
  const staleDetail = await readJSON(page, `/v1/memories/${preferenceID}`);
  const externalMutation = await readJSON(page, `/v1/memories/${preferenceID}/feedback`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': `memory-e2e-external-${preferenceID}`,
    },
    body: JSON.stringify({
      action: 'pin',
      expected_version: staleDetail.body.state_version,
    }),
  });
  assert.equal(externalMutation.status, 201);
  await page.getByRole('button', { name: 'Pin', exact: true }).click();
  await page
    .getByText('Another client changed this memory. Reload the latest version before acting.', {
      exact: true,
    })
    .waitFor();
  await page.getByRole('button', { name: 'Reload', exact: true }).click();
  await page.getByRole('button', { name: 'Unpin', exact: true }).waitFor();
  console.log('✓ version conflict and reload');

  await page.goto(`${baseURL}/memories/${artifactID}`, {
    waitUntil: 'domcontentloaded',
  });
  await page.getByRole('link', { name: sourceFileID, exact: true }).waitFor();
  await page.getByRole('button', { name: 'Forget live memory', exact: true }).click();
  const forgetDialog = page.getByRole('dialog');
  await forgetDialog
    .getByText(
      'This action does not delete an independent source file. Backups and replicas remain subject to the deployment retention and deletion policy. Remove the source separately from Drive if needed.',
      { exact: true },
    )
    .waitFor();
  const confirmForget = forgetDialog.getByRole('button', {
    name: 'Clear live memory',
    exact: true,
  });
  assert.equal(await confirmForget.isDisabled(), true);
  await forgetDialog.getByLabel('Irreversible clear confirmation').fill('FORGET');
  assert.equal(await confirmForget.isEnabled(), true);
  const forgetResponsePromise = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/v1/memories/${artifactID}/forget`) &&
      response.request().method() === 'POST',
  );
  await confirmForget.click();
  const forgetResponse = await forgetResponsePromise;
  assert.equal(forgetResponse.status(), 201);
  const forgetBody = await forgetResponse.json();
  assert.equal(forgetBody.memory_id, artifactID);
  assert.equal(typeof forgetBody.state_version, 'number');
  assert.equal('tombstone' in forgetBody, false);
  assert.equal('content' in forgetBody, false);
  assert.equal('path' in forgetBody, false);
  await page.waitForURL((url) => url.pathname === '/memories');

  const forgottenDetail = await readJSON(page, `/v1/memories/${artifactID}`);
  assert.equal(forgottenDetail.status, 410);
  const afterForget = await readJSON(page, '/v1/memories?lifecycle=all&limit=100');
  assert.equal(
    afterForget.body.memories.some((memory) => memory.id === artifactID),
    false,
    'forgotten memory must disappear from every list',
  );
  console.log('✓ live-memory forget semantics');

  await page.goto(`${baseURL}/files/${sourceFileID}`, {
    waitUntil: 'domcontentloaded',
  });
  await page.getByText('IMG_2012_DALI_0427.jpg', { exact: true }).first().waitFor();
  console.log('✓ source file preserved');

  const mobileContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
  });
  await addMockSession(mobileContext, 'mock-mobile');
  const mobilePage = await mobileContext.newPage();
  await mobilePage.goto(`${baseURL}/memories/${decisionID}`, {
    waitUntil: 'domcontentloaded',
  });
  await mobilePage.getByLabel('Memory detail').waitFor();
  await mobilePage.getByRole('button', { name: 'Back to memory ledger' }).waitFor();
  assert.equal(
    await mobilePage.locator('section[aria-label="Memory filters"]').isVisible(),
    false,
    'detail-first narrow layout must hide the ledger filters',
  );
  const mobileOverflow = await mobilePage.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  assert.ok(mobileOverflow <= 1, `narrow layout overflows horizontally by ${mobileOverflow}px`);
  await mobilePage.screenshot({
    path: path.join(artifactDir, 'memory-detail-mobile.png'),
    fullPage: true,
  });
  await mobilePage.getByRole('button', { name: 'Back to memory ledger' }).click();
  await mobilePage.waitForURL((url) => url.pathname === '/memories');
  await mobilePage.getByTestId(`memory-${decisionID}`).waitFor();
  await mobileContext.close();
  console.log('✓ narrow detail/list layout');

  const readOnlyContext = await browser.newContext({
    viewport: { width: 1100, height: 800 },
  });
  await addMockSession(readOnlyContext, 'mock-readonly', 'readonly@mem.dev');
  const readOnlyPage = await readOnlyContext.newPage();
  await readOnlyPage.goto(`${baseURL}/memories/${decisionID}`, {
    waitUntil: 'domcontentloaded',
  });
  await readOnlyPage.getByLabel('Memory detail').waitFor();
  await readOnlyPage
    .getByText('The current token lacks write permission', { exact: true })
    .waitFor();
  await readOnlyPage
    .getByText('Clearing live memory requires delete permission and an owner/admin role', {
      exact: true,
    })
    .waitFor();
  assert.equal(
    await readOnlyPage.getByRole('button', { name: 'Unpin', exact: true }).isDisabled(),
    true,
  );
  assert.equal(
    await readOnlyPage.getByRole('button', { name: 'Archive', exact: true }).isDisabled(),
    true,
  );
  assert.equal(
    await readOnlyPage
      .getByRole('button', { name: 'Forget live memory', exact: true })
      .isDisabled(),
    true,
  );
  await readOnlyContext.close();

  const noDeleteContext = await browser.newContext({
    viewport: { width: 1100, height: 800 },
  });
  await addMockSession(noDeleteContext, 'mock-no-delete', 'writer@mem.dev');
  const noDeletePage = await noDeleteContext.newPage();
  await noDeletePage.goto(`${baseURL}/memories/${decisionID}`, {
    waitUntil: 'domcontentloaded',
  });
  await noDeletePage.getByLabel('Memory detail').waitFor();
  assert.equal(
    await noDeletePage.getByRole('button', { name: 'Archive', exact: true }).isEnabled(),
    true,
  );
  assert.equal(
    await noDeletePage
      .getByRole('button', { name: 'Forget live memory', exact: true })
      .isDisabled(),
    true,
  );
  await noDeleteContext.close();

  const noReadContext = await browser.newContext({
    viewport: { width: 1100, height: 800 },
  });
  await addMockSession(noReadContext, 'mock-no-read', 'blind@mem.dev');
  const noReadPage = await noReadContext.newPage();
  await noReadPage.goto(`${baseURL}/memories`, { waitUntil: 'domcontentloaded' });
  await noReadPage.getByText('Memory read permission required', { exact: true }).waitFor();
  await noReadContext.close();
  console.log('✓ read/write/delete permission gates');

  assert.deepEqual(pageErrors, [], `page errors: ${pageErrors.join('\n')}`);
  const expectedHTTPFailures = consoleErrors.filter((message) =>
    /status of (400|409|410) \((Bad Request|Conflict|Gone)\)/.test(message),
  );
  assert.ok(
    expectedHTTPFailures.some((message) => message.includes('400 (Bad Request)')),
    'the mismatched-cursor scenario should exercise an HTTP 400',
  );
  assert.ok(
    expectedHTTPFailures.some((message) => message.includes('409 (Conflict)')),
    'the conflict scenario should exercise an HTTP 409',
  );
  assert.ok(
    expectedHTTPFailures.some((message) => message.includes('410 (Gone)')),
    'the forgotten-detail scenario should exercise an HTTP 410',
  );
  assert.deepEqual(
    consoleErrors.filter((message) => !expectedHTTPFailures.includes(message)),
    [],
    `unexpected console errors: ${consoleErrors.join('\n')}`,
  );

  console.log(
    `Memory acceptance passed. Screenshots: ${artifactDir}/memory-ledger-detail.png, ${artifactDir}/memory-detail-mobile.png`,
  );
} catch (error) {
  failure = error;
  failed = true;
} finally {
  try {
    await closeBrowserWithin(browser);
  } catch (error) {
    if (failed) {
      console.warn(`Browser cleanup also failed: ${error instanceof Error ? error.message : error}`);
    } else {
      failure = error;
      failed = true;
    }
  }
  try {
    await stopProcess(vite.child);
  } catch (error) {
    if (failed) {
      console.warn(`Vite cleanup also failed: ${error instanceof Error ? error.message : error}`);
    } else {
      failure = error;
      failed = true;
    }
  }
}

if (failed) throw failure;

// Playwright/Chrome can leave a transient platform handle behind even after
// both child processes are gone. Reaching this line means every assertion and
// cleanup step succeeded, so terminate the standalone acceptance runner.
process.exit(0);
