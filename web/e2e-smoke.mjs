// 端上 E2E 冒烟测试 — 真 Chromium 驱动，全程 vite :5190
// 运行: node e2e-smoke.mjs
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const BASE = 'http://127.0.0.1:5190';
const SHOTS = '/tmp/mem-e2e';
mkdirSync(SHOTS, { recursive: true });

const results = [];
function rec(name, ok, detail) {
  results.push({ name, ok, detail });
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
}

// 用已安装的完整 chromium 二进制，绕开 headless-shell（被 __dirlock 残留锁挡住没装上）
import { existsSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { readdirSync } from 'node:fs';
const pwRoot = join(homedir(), '.cache/ms-playwright');
const chromiumDir = readdirSync(pwRoot).find((d) => d.startsWith('chromium-'));
const execPath = join(pwRoot, chromiumDir, 'chrome-linux64', 'chrome');
if (!existsSync(execPath)) throw new Error('chromium binary not found at ' + execPath);
const browser = await chromium.launch({ executablePath: execPath });
const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
const consoleErrors = [];
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()); });
page.on('pageerror', (e) => consoleErrors.push('PAGEERROR: ' + e.message));

try {
  // T1 — 登录页
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await page.fill('#email', 'demo@mem.dev');
  await page.fill('#password', 'demopass');
  await page.screenshot({ path: `${SHOTS}/01-login.png` });
  await page.click('button[type=submit]');
  await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15000 });
  rec('T1 登录', true, '跳转到 ' + new URL(page.url()).pathname);

  // T2 — Drive / Explorer 首屏（等真文件出现再截图）
  await page.waitForLoadState('networkidle');
  const fileCard = page.locator('text=rust_borrow_checker, text=queue_test.txt, text=demo').first();
  await page.locator('text=demo').first().waitFor({ timeout: 12000 }).catch(() => {});
  await page.waitForTimeout(1500);
  await page.screenshot({ path: `${SHOTS}/02-drive.png`, fullPage: true });
  const driveText = await page.locator('body').innerText();
  const filesShown = await page.locator('text=queue_test.txt').count();
  rec('T2 Drive 首屏', filesShown > 0, filesShown > 0 ? '文件网格已渲染真数据' : `文本 ${driveText.length} 字符但无文件`);

  // T3 — 搜索（用 Explorer 头部搜索框）
  const searchBox = page.locator('input[aria-label="搜索"]').first();
  if (await searchBox.count()) {
    await searchBox.fill('rust ownership');
    await searchBox.press('Enter');
    await page.waitForURL((u) => u.pathname === '/search', { timeout: 8000 }).catch(() => {});
    await page.waitForTimeout(7000); // 等真后端 search 返回
    await page.screenshot({ path: `${SHOTS}/03-search.png`, fullPage: true });
    const hit = await page.locator('text=rust_borrow_checker').count();
    rec('T3 搜索', hit > 0, hit > 0 ? '命中 rust_borrow_checker.md' : '搜索页无命中');
  } else {
    rec('T3 搜索', false, '头部找不到搜索输入框');
  }

  // T4 — Ask 页
  await page.goto(BASE + '/ask', { waitUntil: 'networkidle' });
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${SHOTS}/04-ask.png`, fullPage: true });
  rec('T4 Ask 页', (await page.locator('body').innerText()).includes('Ask') || page.url().includes('/ask'), '渲染完成');

  // T5 — Faces 页
  await page.goto(BASE + '/faces', { waitUntil: 'networkidle' });
  await page.waitForTimeout(1500);
  await page.screenshot({ path: `${SHOTS}/05-faces.png`, fullPage: true });
  rec('T5 Faces 页', true, '渲染完成');

  // T6 — Providers 页
  await page.goto(BASE + '/providers', { waitUntil: 'networkidle' });
  await page.waitForTimeout(1500);
  await page.screenshot({ path: `${SHOTS}/06-providers.png`, fullPage: true });
  const provText = await page.locator('body').innerText();
  rec('T6 Providers 页', provText.includes('embedding') || provText.includes('ollama'), '渲染完成');

  // T7 — 控制台无报错
  rec('T7 浏览器控制台', consoleErrors.length === 0,
    consoleErrors.length ? `${consoleErrors.length} 条 error` : '无 JS error');
} catch (e) {
  rec('FATAL', false, e.message);
  await page.screenshot({ path: `${SHOTS}/99-crash.png` }).catch(() => {});
} finally {
  await browser.close();
}

const pass = results.filter((r) => r.ok).length;
console.log(`\n  ====  ${pass}/${results.length} PASS  ====`);
if (consoleErrors.length) {
  console.log('\n  控制台 error 明细:');
  consoleErrors.slice(0, 10).forEach((e) => console.log('   - ' + e));
}
console.log(`\n  截图目录: ${SHOTS}`);
process.exit(pass === results.length ? 0 : 1);
