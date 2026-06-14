import * as React from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Logo } from '@/components/layout/Logo';
import { useAuth } from '@/hooks/useAuth';
import { ApiException } from '@/lib/api';
import { toast } from 'sonner';

type Mode = 'login' | 'register';

export function LoginPage() {
  const { token, login, register, loading } = useAuth();
  const navigate = useNavigate();
  const [mode, setMode] = React.useState<Mode>('login');
  const [email, setEmail] = React.useState('');
  const [password, setPassword] = React.useState('');
  const [error, setError] = React.useState<string | null>(null);

  if (token) return <Navigate to="/" replace />;

  const isRegister = mode === 'register';

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      if (isRegister) {
        await register(email, password);
        toast.success('账号已创建');
      } else {
        await login(email, password);
      }
      navigate('/', { replace: true });
    } catch (err) {
      // Prefer the backend's human-readable hint over the error code.
      const msg =
        err instanceof ApiException
          ? err.hint || err.message
          : err instanceof Error
            ? err.message
            : '请稍后重试';
      setError(msg);
      toast.error(isRegister ? '注册失败' : '登录失败', { description: msg });
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-bg relative overflow-hidden">
      <div className="absolute inset-0 dot-grid opacity-40" aria-hidden="true" />
      <div
        className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2
                   h-[600px] w-[600px] rounded-full bg-accent/10 blur-[120px]"
        aria-hidden="true"
      />
      <div className="relative z-10 w-[400px] max-w-[92vw]">
        <div className="mb-8 flex flex-col items-center gap-3">
          <Logo className="scale-125" />
          <p className="text-sm text-fg-muted">Agent-Native AI 网盘</p>
        </div>
        <form onSubmit={onSubmit} className="surface p-6 flex flex-col gap-4 shadow-soft">
          {/* Mode switch */}
          <div className="flex rounded-lg bg-bg-inset p-1 text-sm">
            {(['login', 'register'] as Mode[]).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => {
                  setMode(m);
                  setError(null);
                }}
                className={
                  'flex-1 rounded-md py-1.5 font-medium transition-colors ' +
                  (mode === m ? 'bg-bg-panel text-fg shadow-soft' : 'text-fg-muted hover:text-fg')
                }
              >
                {m === 'login' ? '登录' : '注册'}
              </button>
            ))}
          </div>

          <div className="flex flex-col gap-1.5">
            <label className="text-xs text-fg-muted" htmlFor="email">
              邮箱
            </label>
            <Input
              id="email"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@example.com"
              autoComplete="email"
              required
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <label className="text-xs text-fg-muted" htmlFor="password">
              密码
            </label>
            <Input
              id="password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={isRegister ? '至少 6 位' : '••••••••'}
              autoComplete={isRegister ? 'new-password' : 'current-password'}
              minLength={isRegister ? 6 : undefined}
              required
            />
          </div>

          {error && (
            <div className="text-xs text-danger bg-danger/10 border border-danger/30 rounded-md px-3 py-2">
              {error}
            </div>
          )}

          <Button type="submit" variant="primary" size="lg" loading={loading} className="mt-1">
            {isRegister ? '创建账号' : '登录'}
          </Button>

          <div className="text-center text-xs text-fg-subtle">
            {isRegister ? (
              <>
                已有账号？
                <button
                  type="button"
                  className="text-accent hover:underline ml-1"
                  onClick={() => {
                    setMode('login');
                    setError(null);
                  }}
                >
                  去登录
                </button>
              </>
            ) : (
              <>
                还没有账号？
                <button
                  type="button"
                  className="text-accent hover:underline ml-1"
                  onClick={() => {
                    setMode('register');
                    setError(null);
                  }}
                >
                  立即注册
                </button>
              </>
            )}
          </div>
        </form>
        <p className="mt-4 text-center text-xs text-fg-subtle">
          自部署版本 · 数据全在本地 · Apache-2.0
        </p>
      </div>
    </div>
  );
}
