import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const webRoot = path.dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.LOCALIZATION_E2E_PORT ?? 5197);
const baseURL = `http://127.0.0.1:${port}`;
const artifactDir = path.join(tmpdir(), `mem-localization-acceptance-${process.pid}`);
const decisionID = '30000000-0000-4000-8000-000000000001';

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

async function closeBrowserWithin(activeBrowser, timeoutMs = 5_000) {
  if (!activeBrowser) return;
  let timeout;
  try {
    await Promise.race([
      activeBrowser.close(),
      new Promise((resolve) => {
        timeout = setTimeout(resolve, timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timeout);
  }
}

async function addMockSession(context) {
  await context.addInitScript(() => {
    localStorage.setItem('mem.token', 'mock-e2e-token');
    localStorage.setItem(
      'mem.user',
      JSON.stringify({
        id: 'user-1',
        email: 'localization-e2e@mem.dev',
        created_at: '2026-01-01T00:00:00Z',
      }),
    );
    if (!localStorage.getItem('mem.lang')) localStorage.setItem('mem.lang', 'en');
    document.addEventListener(
      'readystatechange',
      () => {
        if (document.readyState === 'interactive') {
          window.__documentLangAtInteractive = document.documentElement.lang;
        }
      },
      { once: true },
    );
  });
}

async function openAccount(page, label) {
  await page.getByRole('button', { name: label }).click();
}

const vite = startVite();
let browser;
let failure;

try {
  await waitForServer(vite);
  await mkdir(artifactDir, { recursive: true });
  browser = await launchBrowser();
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await addMockSession(context);

  const page = await context.newPage();
  page.setDefaultTimeout(12_000);
  page.setDefaultNavigationTimeout(20_000);
  const pageErrors = [];
  page.on('pageerror', (error) => {
    pageErrors.push(error.message);
    console.error(`page error: ${error.message}`);
  });

  await page.goto(`${baseURL}/providers`, { waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: 'Index models' }).waitFor();
  await page.getByText('Text retrieval', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => document.documentElement.lang), 'en');
  assert.equal(await page.evaluate(() => window.__documentLangAtInteractive), 'en');
  assert.equal(await page.title(), 'mem · Agent-native AI drive');
  await page.screenshot({
    path: path.join(artifactDir, 'providers-en.png'),
    fullPage: true,
  });
  console.log('✓ English provider fields and pre-React document language');

  await openAccount(page, 'Account menu');
  await page.getByRole('menuitem', { name: '中文', exact: true }).click();
  await page.locator('h1').filter({ hasText: '索引模型' }).waitFor();
  await page.getByText('文本检索', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => document.documentElement.lang), 'zh-CN');
  assert.equal(await page.evaluate(() => localStorage.getItem('mem.lang')), 'zh');
  assert.equal(await page.title(), 'mem · Agent 原生 AI 网盘');
  await page.screenshot({
    path: path.join(artifactDir, 'providers-zh.png'),
    fullPage: true,
  });
  console.log('✓ runtime switch updates all provider chrome and persists Chinese');

  await page.evaluate(() => localStorage.setItem('mem.token', 'mock-managed-embedding-500'));
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  const entitlementError = page.getByTestId('managed-embedding-entitlement-error');
  await entitlementError.waitFor();
  const entitlementErrorText = await entitlementError.textContent();
  assert.match(
    entitlementErrorText ?? '',
    /无法加载 Embedding 状态: 请重试，或使用本地 \/ BYOM Provider。/,
  );
  assert.doesNotMatch(entitlementErrorText ?? '', /Try again or use a local\/BYOM provider\./);
  await page.evaluate(() => localStorage.setItem('mem.token', 'mock-e2e-token'));
  console.log('✓ unknown managed-embedding errors use the selected locale');

  await page.goto(`${baseURL}/search`, { waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: '搜索' }).waitFor();
  await page.getByRole('button', { name: '草地上的金毛' }).waitFor();
  assert.equal(await page.evaluate(() => window.__documentLangAtInteractive), 'zh-CN');

  await page.goto(`${baseURL}/memories`, { waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: '记忆账本' }).waitFor();
  await page.getByTestId(`memory-${decisionID}`).waitFor();
  await page.screenshot({
    path: path.join(artifactDir, 'memory-zh.png'),
    fullPage: true,
  });
  console.log('✓ Chinese search examples, statuses, filters and memory ledger');

  await openAccount(page, '账户菜单');
  await page.getByRole('menuitem', { name: 'English', exact: true }).click();
  await page.locator('h1').filter({ hasText: 'Memory ledger' }).waitFor();
  assert.equal(await page.evaluate(() => document.documentElement.lang), 'en');
  assert.equal(await page.evaluate(() => localStorage.getItem('mem.lang')), 'en');
  console.log('✓ English runtime switch updates the memory ledger');

  await page.getByTestId(`memory-${decisionID}`).click();
  console.log('✓ English memory detail opened');
  await page.getByRole('button', { name: 'Forget live memory' }).click();
  await page.getByRole('heading', { name: 'Irreversibly clear this live memory?' }).waitFor();
  await page.getByRole('button', { name: 'Cancel' }).click();
  console.log('✓ destructive dialog follows English');

  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.locator('h1').filter({ hasText: 'Memory ledger' }).waitFor();
  assert.equal(await page.evaluate(() => window.__documentLangAtInteractive), 'en');
  assert.deepEqual(pageErrors, [], `unexpected page errors: ${pageErrors.join('\n')}`);

  await context.close();
  console.log(`Localization acceptance passed. Screenshots: ${artifactDir}`);
} catch (error) {
  failure = error;
  process.exitCode = 1;
} finally {
  await closeBrowserWithin(browser);
  await stopProcess(vite.child);
}

if (failure) throw failure;
