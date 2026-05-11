import { createBrowserRouter, Navigate } from 'react-router-dom';
import { LoginGate } from './LoginGate';
import { LoginPage } from '@/pages/LoginPage';
import { ExplorerPage } from '@/pages/ExplorerPage';
import { FileDetailPage } from '@/pages/FileDetailPage';
import { NotFoundPage } from '@/pages/NotFoundPage';

// Phase 2 — kept around but not routed.
// import { SearchPage } from '@/pages/SearchPage';
// import { SettingsPage } from '@/pages/SettingsPage';

export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  // Index → /drive
  { path: '/', element: <Navigate to="/drive" replace /> },

  // Explorer: catch-all under /drive so /drive/Photos/2012 etc all hit the same page.
  {
    path: '/drive/*',
    element: (
      <LoginGate>
        <ExplorerPage />
      </LoginGate>
    ),
  },

  // File detail — kept minimal.
  {
    path: '/files/:id',
    element: (
      <LoginGate>
        <FileDetailPage />
      </LoginGate>
    ),
  },

  { path: '*', element: <NotFoundPage /> },
]);
