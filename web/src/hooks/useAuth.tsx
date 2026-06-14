import * as React from 'react';
import type { User, AuthLoginResponse } from '@/lib/types';
import { api, clearToken, getToken, setToken } from '@/lib/api';

interface AuthContextValue {
  user: User | null;
  token: string | null;
  loading: boolean;
  login: (email: string, password: string) => Promise<void>;
  register: (email: string, password: string) => Promise<void>;
  logout: () => void;
}

const AuthCtx = React.createContext<AuthContextValue | null>(null);
const USER_KEY = 'mem.user';

function readUser(): User | null {
  const raw = localStorage.getItem(USER_KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as User;
  } catch {
    return null;
  }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [token, setTokenState] = React.useState<string | null>(() => getToken());
  const [user, setUser] = React.useState<User | null>(() => readUser());
  const [loading, setLoading] = React.useState(false);

  const persistSession = React.useCallback((res: AuthLoginResponse) => {
    setToken(res.token);
    localStorage.setItem(USER_KEY, JSON.stringify(res.user));
    setTokenState(res.token);
    setUser(res.user);
  }, []);

  const login = React.useCallback(
    async (email: string, password: string) => {
      setLoading(true);
      try {
        persistSession(await api.post<AuthLoginResponse>('/auth/login', { email, password }));
      } finally {
        setLoading(false);
      }
    },
    [persistSession],
  );

  const register = React.useCallback(
    async (email: string, password: string) => {
      setLoading(true);
      try {
        persistSession(await api.post<AuthLoginResponse>('/auth/register', { email, password }));
      } finally {
        setLoading(false);
      }
    },
    [persistSession],
  );

  const logout = React.useCallback(() => {
    clearToken();
    localStorage.removeItem(USER_KEY);
    setTokenState(null);
    setUser(null);
  }, []);

  const value = React.useMemo<AuthContextValue>(
    () => ({ user, token, loading, login, register, logout }),
    [user, token, loading, login, register, logout],
  );

  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = React.useContext(AuthCtx);
  if (!ctx) throw new Error('useAuth must be used within <AuthProvider>');
  return ctx;
}
