import { setupWorker } from 'msw/browser';
import { handlers } from './handlers';

export const worker = setupWorker(...handlers);

export async function startMockWorker(): Promise<void> {
  await worker.start({
    onUnhandledRequest: 'bypass',
    quiet: false,
    serviceWorker: { url: '/mockServiceWorker.js' },
  });

  console.info('[mem] mock service worker started — VITE_USE_MOCK=true');
}
