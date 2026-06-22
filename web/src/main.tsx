import React from 'react';
import ReactDOM from 'react-dom/client';
import { RouterProvider } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Toaster } from 'sonner';
import { AuthProvider } from '@/hooks/useAuth';
import { I18nProvider } from '@/i18n';
import { router } from '@/routes/router';

import './styles/globals.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1,
      staleTime: 30_000,
    },
  },
});

async function enableMocks(): Promise<void> {
  const useMock = (import.meta.env.VITE_USE_MOCK ?? 'true') !== 'false';
  if (!useMock) return;
  const { startMockWorker } = await import('@/mocks/browser');
  await startMockWorker();
}

void enableMocks().then(() => {
  const rootEl = document.getElementById('root');
  if (!rootEl) throw new Error('root element missing');
  ReactDOM.createRoot(rootEl).render(
    <React.StrictMode>
      <QueryClientProvider client={queryClient}>
        <I18nProvider>
        <AuthProvider>
          <RouterProvider router={router} />
          <Toaster
            theme="dark"
            position="bottom-right"
            toastOptions={{
              style: {
                background: 'rgb(18 21 28)',
                border: '1px solid rgb(30 35 46)',
                color: 'rgb(232 234 240)',
              },
            }}
          />
        </AuthProvider>
        </I18nProvider>
      </QueryClientProvider>
    </React.StrictMode>,
  );
});
