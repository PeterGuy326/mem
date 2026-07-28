import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const webRoot = path.dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.TRANSFER_E2E_PORT ?? 5193);
const baseURL = `http://127.0.0.1:${port}`;
const artifactDir = path.join(tmpdir(), `mem-transfer-acceptance-${process.pid}`);
const bundleMIME = 'application/vnd.mem.workspace-bundle+zip';
const exportedBytes = 'PK\u0003\u0004MEM.WORKSPACE_BUNDLE.V1\nmanifest.json\nchecksums.sha256\n';

function startVite() {
  const vite = path.join(webRoot, 'node_modules', 'vite', 'bin', 'vite.js');
  const child = spawn(
    process.execPath,
    [vite, '--host', '127.0.0.1', '--port', String(port), '--strictPort'],
    {
      cwd: webRoot,
      env: { ...process.env, VITE_USE_MOCK: 'true' },
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
      // Vite is still starting.
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
    return chromium.launch({ channel: 'chrome', headless: true });
  }
}

async function addMockSession(context, token, email = 'transfer-e2e@mem.dev') {
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

async function chooseBundle(page, name, body, mimeType = bundleMIME) {
  await page.getByTestId('workspace-import-input').setInputFiles({
    name,
    mimeType,
    buffer: Buffer.from(body),
  });
}

async function confirmAndImport(page) {
  const checkbox = page.getByRole('checkbox');
  assert.equal(await checkbox.isChecked(), false);
  await checkbox.check();
  const button = page.getByTestId('workspace-import-button');
  assert.equal(await button.isEnabled(), true);
  await button.click();
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
    viewport: { width: 1440, height: 1050 },
    acceptDownloads: true,
  });
  await addMockSession(context, 'mock-transfer-e2e');
  const page = await context.newPage();
  page.setDefaultTimeout(10_000);
  page.setDefaultNavigationTimeout(15_000);
  const pageErrors = [];
  const consoleErrors = [];
  const dialogs = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('dialog', async (dialog) => {
    dialogs.push(dialog.message());
    await dialog.dismiss();
  });

  await page.goto(`${baseURL}/transfer`, { waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: 'Workspace transfer' }).waitFor();
  await page.getByRole('link', { name: 'Transfer' }).waitFor();
  await page.getByText('empty workspace only', { exact: true }).waitFor();
  await page.getByText('merge unavailable', { exact: true }).waitFor();
  await page
    .getByText(
      'The target workspace must contain no portable data. Existing content is never overwritten or merged; any conflict prevents the entire import from committing.',
      { exact: true },
    )
    .waitFor();
  const mediaTypeChecks = await page.evaluate(async () => {
    const { WORKSPACE_BUNDLE_MEDIA_TYPE, isWorkspaceBundleMediaType } =
      await import('/src/lib/workspace-transfer.ts');
    return {
      canonical: WORKSPACE_BUNDLE_MEDIA_TYPE,
      acceptsCanonical: isWorkspaceBundleMediaType(WORKSPACE_BUNDLE_MEDIA_TYPE),
      rejectsVersionParameter: isWorkspaceBundleMediaType(
        `${WORKSPACE_BUNDLE_MEDIA_TYPE}; version=1`,
      ),
      rejectsCharsetParameter: isWorkspaceBundleMediaType(
        `${WORKSPACE_BUNDLE_MEDIA_TYPE}; charset=binary`,
      ),
    };
  });
  assert.deepEqual(mediaTypeChecks, {
    canonical: bundleMIME,
    acceptsCanonical: true,
    rejectsVersionParameter: false,
    rejectsCharsetParameter: false,
  });
  console.log('✓ canonical parameter-free workspace bundle media type');
  await page.screenshot({
    path: path.join(artifactDir, 'workspace-transfer-desktop.png'),
    fullPage: true,
  });
  console.log('✓ discoverable transfer route and fresh-only contract');

  const downloadPromise = page.waitForEvent('download');
  const exportResponsePromise = page.waitForResponse(
    (response) =>
      response.url().includes('/v1/workspaces/current/export') &&
      response.request().method() === 'GET',
  );
  await page.getByTestId('workspace-export-button').click();
  const [download, exportResponse] = await Promise.all([downloadPromise, exportResponsePromise]);
  assert.equal(await exportResponse.headerValue('content-type'), bundleMIME);
  assert.equal(download.suggestedFilename(), 'workspace-agent-drive-demo.membundle');
  const exportPath = path.join(artifactDir, download.suggestedFilename());
  await download.saveAs(exportPath);
  assert.deepEqual(await readFile(exportPath), Buffer.from(exportedBytes));
  await page.getByTestId('workspace-export-success').waitFor();
  console.log('✓ export bytes, MIME validation, and safe save');

  const filenameChecks = await page.evaluate(async () => {
    const { safeWorkspaceBundleFilename } = await import('/src/lib/workspace-transfer.ts');
    return {
      fallback: safeWorkspaceBundleFilename(null, 'workspace/../../unsafe'),
      reserved: safeWorkspaceBundleFilename('attachment; filename="CON.membundle"', 'workspace-1'),
      traversal: safeWorkspaceBundleFilename(
        "attachment; filename*=UTF-8''..%2F..%2Fevil.membundle",
        'workspace-1',
      ),
    };
  });
  assert.match(
    filenameChecks.fallback,
    /^workspace-workspace-unsafe-\d{4}-\d{2}-\d{2}\.membundle$/,
  );
  assert.notEqual(filenameChecks.reserved.toLowerCase(), 'con.membundle');
  assert.equal(filenameChecks.traversal, 'evil.membundle');
  console.log('✓ filename traversal, reserved name, and fallback handling');

  await chooseBundle(page, 'agent-drive.membundle', 'PK\u0003\u0004MOCK_IMPORT_SUCCESS');
  await page.getByTestId('workspace-import-file').getByText('agent-drive.membundle').waitFor();
  await page
    .getByTestId('workspace-import-file')
    .getByText(/B · application\/vnd\.mem/)
    .waitFor();
  assert.equal(await page.getByTestId('workspace-import-button').isDisabled(), true);
  const importResponse = page.waitForResponse(
    (response) =>
      response.url().includes('/v1/workspaces/current/import?mode=fresh') &&
      response.request().method() === 'POST',
  );
  await confirmAndImport(page);
  const completedImportResponse = await importResponse;
  assert.equal(completedImportResponse.status(), 200);
  assert.equal(await completedImportResponse.request().headerValue('content-type'), bundleMIME);
  const success = page.getByTestId('workspace-import-success');
  await success.getByText('Workspace restored atomically', { exact: true }).waitFor();
  await success.getByText('12', { exact: true }).waitFor();
  await success.getByText('3.0 MB', { exact: true }).waitFor();
  await success.getByText('c'.repeat(64), { exact: true }).waitFor();
  console.log('✓ guarded raw upload and success counts/hash');

  const replayResponse = page.waitForResponse(
    (response) =>
      response.url().includes('/v1/workspaces/current/import?mode=fresh') &&
      response.request().method() === 'POST',
  );
  await page.getByTestId('workspace-import-button').click();
  assert.equal((await replayResponse).status(), 200);
  await success
    .getByText('This archive was already imported successfully', { exact: true })
    .waitFor();
  await success.getByText('safe replay', { exact: true }).waitFor();
  console.log('✓ idempotent server replay surfaced');

  await chooseBundle(page, 'conflict.membundle', 'PK\u0003\u0004MOCK_IMPORT_CONFLICT');
  await confirmAndImport(page);
  const conflicts = page.getByTestId('transfer-conflicts');
  await conflicts
    .getByText('Fresh import rejected because the target is not empty', {
      exact: true,
    })
    .waitFor();
  await conflicts.getByText('<img src=x onerror=alert("conflict-xss")>', { exact: true }).waitFor();
  await conflicts.getByText('/Projects/<script>alert("never")</script>', { exact: true }).waitFor();
  await page
    .getByTestId('transfer-conflicts-truncated')
    .getByText(/at least 202 conflicts are confirmed and 2 are shown/)
    .waitFor();
  assert.deepEqual(dialogs, [], 'untrusted conflict values must remain inert text');
  assert.equal(
    await page.locator('img[src="x"]').count(),
    0,
    'conflict strings must not create executable image nodes',
  );
  console.log('✓ structured, truncated conflict details and inert XSS payloads');

  await chooseBundle(page, 'not-a-bundle.zip', 'PK\u0003\u0004BAD_EXTENSION', 'application/zip');
  await page
    .getByTestId('workspace-import-file-error')
    .getByText(/\.membundle extension/)
    .waitFor();
  assert.equal(await page.getByTestId('workspace-import-button').isDisabled(), true);
  await chooseBundle(page, 'wrong-mime.membundle', 'PK\u0003\u0004BAD_MIME', 'application/zip');
  await page
    .getByTestId('workspace-import-file-error')
    .getByText(/incompatible MIME/)
    .waitFor();
  assert.equal(await page.getByTestId('workspace-import-button').isDisabled(), true);
  console.log('✓ local extension and MIME validation');

  const errorCases = [
    {
      name: 'too-large.membundle',
      marker: 'MOCK_IMPORT_TOO_LARGE',
      testID: 'transfer-error-too_large',
      text: 'Archive exceeds the upload limit',
    },
    {
      name: 'invalid.membundle',
      marker: 'MOCK_IMPORT_INVALID',
      testID: 'transfer-error-invalid',
      text: 'Archive validation failed',
    },
    {
      name: 'unsupported.membundle',
      marker: 'MOCK_IMPORT_UNSUPPORTED',
      testID: 'transfer-error-unsupported',
      text: 'Unsupported protocol or response',
    },
    {
      name: 'server-error.membundle',
      marker: 'MOCK_IMPORT_SERVER_ERROR',
      testID: 'transfer-error-server',
      text: 'The server could not complete the transfer',
    },
  ];
  for (const errorCase of errorCases) {
    await chooseBundle(page, errorCase.name, `PK\u0003\u0004${errorCase.marker}`);
    await confirmAndImport(page);
    const notice = page.getByTestId(errorCase.testID);
    await notice.getByText(errorCase.text, { exact: true }).waitFor();
    await notice.getByRole('button', { name: 'Retry' }).waitFor();
  }
  console.log('✓ distinct 413, 400, 422, and 500 errors with retry');

  const hostileContext = await browser.newContext({
    viewport: { width: 1100, height: 820 },
    acceptDownloads: true,
  });
  await addMockSession(hostileContext, 'mock-transfer-hostile-name');
  const hostilePage = await hostileContext.newPage();
  await hostilePage.goto(`${baseURL}/transfer`, { waitUntil: 'domcontentloaded' });
  await hostilePage.getByRole('heading', { name: 'Workspace transfer' }).waitFor();
  const hostileDownloadPromise = hostilePage.waitForEvent('download');
  await hostilePage.getByTestId('workspace-export-button').click();
  const hostileDownload = await hostileDownloadPromise;
  assert.match(hostileDownload.suggestedFilename(), /^[^/\\<>:"|?*]+\.membundle$/);
  await hostileContext.close();

  const badMIMEContext = await browser.newContext({
    viewport: { width: 1100, height: 820 },
    acceptDownloads: true,
  });
  await addMockSession(badMIMEContext, 'mock-transfer-bad-mime');
  const badMIMEPage = await badMIMEContext.newPage();
  let badMIMEDownloads = 0;
  badMIMEPage.on('download', () => {
    badMIMEDownloads += 1;
  });
  await badMIMEPage.goto(`${baseURL}/transfer`, { waitUntil: 'domcontentloaded' });
  await badMIMEPage.getByTestId('workspace-export-button').click();
  await badMIMEPage.getByTestId('transfer-error-unsupported').waitFor();
  assert.equal(badMIMEDownloads, 0, 'unsupported export response must never be saved');
  await badMIMEContext.close();
  console.log('✓ hostile filename sanitized and unsupported export never saved');

  const permissionContext = await browser.newContext({
    viewport: { width: 1100, height: 820 },
  });
  await addMockSession(permissionContext, 'mock-transfer-readonly');
  const permissionPage = await permissionContext.newPage();
  await permissionPage.goto(`${baseURL}/transfer`, { waitUntil: 'domcontentloaded' });
  await permissionPage.getByRole('heading', { name: 'Workspace transfer' }).waitFor();
  assert.equal(await permissionPage.getByTestId('transfer-permission-gate').count(), 2);
  assert.equal(await permissionPage.getByTestId('workspace-export-button').isDisabled(), true);
  assert.equal(await permissionPage.getByTestId('workspace-import-input').isDisabled(), true);
  await permissionContext.close();

  const unsupportedContext = await browser.newContext({
    viewport: { width: 1100, height: 820 },
  });
  await addMockSession(unsupportedContext, 'mock-transfer-unsupported');
  const unsupportedPage = await unsupportedContext.newPage();
  await unsupportedPage.goto(`${baseURL}/transfer`, {
    waitUntil: 'domcontentloaded',
  });
  await unsupportedPage.getByText('Workspace transfer is not enabled', { exact: true }).waitFor();
  assert.equal(await unsupportedPage.getByTestId('workspace-export-button').count(), 0);
  await unsupportedContext.close();
  console.log('✓ permission and feature/capability gates');

  const mobileContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
  });
  await addMockSession(mobileContext, 'mock-transfer-mobile');
  const mobilePage = await mobileContext.newPage();
  await mobilePage.goto(`${baseURL}/transfer`, { waitUntil: 'domcontentloaded' });
  await mobilePage.getByRole('heading', { name: 'Workspace transfer' }).waitFor();
  const mobileOverflow = await mobilePage.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  assert.ok(
    mobileOverflow <= 1,
    `390px transfer layout overflows horizontally by ${mobileOverflow}px`,
  );
  await mobilePage.screenshot({
    path: path.join(artifactDir, 'workspace-transfer-mobile.png'),
    fullPage: true,
  });
  await mobileContext.close();
  console.log('✓ 390px layout and visual artifact');

  assert.deepEqual(pageErrors, [], `page errors: ${pageErrors.join('\n')}`);
  const expectedHTTPFailures = consoleErrors.filter((message) =>
    /status of (400|409|413|422|500)/.test(message),
  );
  assert.deepEqual(
    consoleErrors.filter((message) => !expectedHTTPFailures.includes(message)),
    [],
    `unexpected console errors: ${consoleErrors.join('\n')}`,
  );

  console.log(
    `Transfer acceptance passed. Screenshots: ${artifactDir}/workspace-transfer-desktop.png, ${artifactDir}/workspace-transfer-mobile.png`,
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

process.exit(0);
