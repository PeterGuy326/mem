import React from 'react';
import ReactDOM from 'react-dom/client';
import { RouterProvider } from 'react-router/dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Toaster } from 'sonner';
import { AuthProvider } from '@/hooks/useAuth';
import { ThemeProvider, useTheme } from '@/hooks/useTheme';
import { I18nProvider } from '@/i18n';
import { ErrorBoundary } from '@/components/ErrorBoundary';
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

function ThemedToaster() {
  const { theme } = useTheme();
  return (
    <div data-toast-theme={theme}>
      <Toaster
        theme={theme}
        position="bottom-right"
        toastOptions={{
          style: {
            background: 'rgb(var(--bg-panel))',
            border: '1px solid rgb(var(--border))',
            color: 'rgb(var(--fg))',
          },
        }}
      />
    </div>
  );
}

async function enableMocks(): Promise<void> {
  const useMock = import.meta.env.VITE_USE_MOCK === 'true';
  if (!useMock) return;
  const { startMockWorker } = await import('@/mocks/browser');
  await startMockWorker();
}

void enableMocks().then(() => {
  const rootEl = document.getElementById('root');
  if (!rootEl) throw new Error('root element missing');
  ReactDOM.createRoot(rootEl).render(
    <React.StrictMode>
      <ErrorBoundary>
        <ThemeProvider>
          <QueryClientProvider client={queryClient}>
            <I18nProvider>
              <AuthProvider>
                <RouterProvider router={router} />
                <ThemedToaster />
              </AuthProvider>
            </I18nProvider>
          </QueryClientProvider>
        </ThemeProvider>
      </ErrorBoundary>
    </React.StrictMode>,
  );
});
