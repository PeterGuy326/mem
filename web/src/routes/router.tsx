import { createBrowserRouter, Navigate } from 'react-router-dom';
import { LoginGate } from './LoginGate';
import { AppLayout } from '@/components/layout/AppLayout';
import { LoginPage } from '@/pages/LoginPage';
import { ExplorerPage } from '@/pages/ExplorerPage';
import { FileDetailPage } from '@/pages/FileDetailPage';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { FacesPage } from '@/pages/FacesPage';
import { ProvidersPage } from '@/pages/ProvidersPage';
import { SearchPage } from '@/pages/SearchPage';

export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  { path: '/', element: <Navigate to="/drive" replace /> },

  // Explorer: catch-all under /drive. Renders its own TopBar + two-pane layout.
  {
    path: '/drive/*',
    element: (
      <LoginGate>
        <ExplorerPage />
      </LoginGate>
    ),
  },

  // Everything else shares the global TopBar via AppLayout.
  {
    element: (
      <LoginGate>
        <AppLayout />
      </LoginGate>
    ),
    children: [
      { path: '/files/:id', element: <FileDetailPage /> },
      { path: '/search', element: <SearchPage /> },
      // Ask is now the global floating assistant (AskWidget); keep the old
      // path working by redirecting to the drive.
      { path: '/ask', element: <Navigate to="/drive" replace /> },
      { path: '/faces', element: <FacesPage /> },
      { path: '/providers', element: <ProvidersPage /> },
    ],
  },

  { path: '*', element: <NotFoundPage /> },
]);
