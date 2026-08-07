import { Navigate, useLocation } from 'react-router';
import { useAuth } from '@/hooks/useAuth';

/** Wrap any private route subtree. Bounces to /login when unauthenticated. */
export function LoginGate({ children }: { children: React.ReactNode }) {
  const { token } = useAuth();
  const location = useLocation();
  if (!token) {
    return <Navigate to="/login" state={{ from: location.pathname }} replace />;
  }
  return <>{children}</>;
}
