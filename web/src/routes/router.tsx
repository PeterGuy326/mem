import { createBrowserRouter, Navigate } from 'react-router';
import { LoginGate } from './LoginGate';
import { AppLayout } from '@/components/layout/AppLayout';
import { LoginPage } from '@/pages/LoginPage';
import { ExplorerPage } from '@/pages/ExplorerPage';
import { FileDetailPage } from '@/pages/FileDetailPage';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { ProvidersPage } from '@/pages/ProvidersPage';
import { PermissionsPage } from '@/pages/PermissionsPage';
import { SearchPage } from '@/pages/SearchPage';
import { TasksPage } from '@/pages/TasksPage';
import { TaskDetailPage } from '@/pages/TaskDetailPage';
import { CheckpointDetailPage } from '@/pages/CheckpointDetailPage';
import { MemoriesPage } from '@/pages/MemoriesPage';
import { TransferPage } from '@/pages/TransferPage';
import { HandoffGate } from './HandoffGate';

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
      // Faces folded into Search (the People row); keep the path working.
      { path: '/faces', element: <Navigate to="/search" replace /> },
      { path: '/providers', element: <ProvidersPage /> },
      { path: '/permissions', element: <PermissionsPage /> },
      { path: '/memories', element: <MemoriesPage /> },
      { path: '/memories/:memoryId', element: <MemoriesPage /> },
      { path: '/transfer', element: <TransferPage /> },
      {
        element: <HandoffGate />,
        children: [
          { path: '/tasks', element: <TasksPage /> },
          {
            path: '/tasks/:taskKey/checkpoints/:checkpointId',
            element: <CheckpointDetailPage />,
          },
          { path: '/tasks/:taskKey', element: <TaskDetailPage /> },
        ],
      },
    ],
  },

  { path: '*', element: <NotFoundPage /> },
]);
