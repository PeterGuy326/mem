import { createBrowserRouter, Navigate } from 'react-router-dom';
import { AppShell } from '@/components/layout/AppShell';
import { LoginGate } from './LoginGate';
import { LoginPage } from '@/pages/LoginPage';
import { UploadPage } from '@/pages/UploadPage';
import { SearchPage } from '@/pages/SearchPage';
import { FileDetailPage } from '@/pages/FileDetailPage';
import { SettingsPage } from '@/pages/SettingsPage';
import { NotFoundPage } from '@/pages/NotFoundPage';

export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  {
    path: '/',
    element: (
      <LoginGate>
        <AppShell />
      </LoginGate>
    ),
    children: [
      { index: true, element: <UploadPage /> },
      { path: 'search', element: <SearchPage /> },
      { path: 'files/:id', element: <FileDetailPage /> },
      { path: 'settings', element: <SettingsPage /> },
      // Phase 2 route placeholders — redirect for now.
      { path: 'timeline', element: <Navigate to="/" replace /> },
      { path: 'faces', element: <Navigate to="/" replace /> },
      { path: 'tags', element: <Navigate to="/" replace /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]);
