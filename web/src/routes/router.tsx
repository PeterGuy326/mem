import { createBrowserRouter, Navigate } from 'react-router-dom';
import { LoginGate } from './LoginGate';
import { LoginPage } from '@/pages/LoginPage';
import { ExplorerPage } from '@/pages/ExplorerPage';
import { FileDetailPage } from '@/pages/FileDetailPage';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { AskPage } from '@/pages/AskPage';
import { FacesPage } from '@/pages/FacesPage';
import { ProvidersPage } from '@/pages/ProvidersPage';
import { SearchPage } from '@/pages/SearchPage';

export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  { path: '/', element: <Navigate to="/drive" replace /> },

  // Explorer: catch-all under /drive.
  {
    path: '/drive/*',
    element: (
      <LoginGate>
        <ExplorerPage />
      </LoginGate>
    ),
  },

  { path: '/files/:id', element: (<LoginGate><FileDetailPage /></LoginGate>) },
  { path: '/search', element: (<LoginGate><SearchPage /></LoginGate>) },
  { path: '/ask', element: (<LoginGate><AskPage /></LoginGate>) },
  { path: '/faces', element: (<LoginGate><FacesPage /></LoginGate>) },
  { path: '/providers', element: (<LoginGate><ProvidersPage /></LoginGate>) },

  { path: '*', element: <NotFoundPage /> },
]);
