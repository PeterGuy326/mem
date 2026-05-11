import * as React from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Logo } from '@/components/layout/Logo';
import { useAuth } from '@/hooks/useAuth';
import { toast } from 'sonner';

export function LoginPage() {
  const { token, login, loading } = useAuth();
  const navigate = useNavigate();
  const [email, setEmail] = React.useState('demo@mem.dev');
  const [password, setPassword] = React.useState('demo');

  if (token) return <Navigate to="/" replace />;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    try {
      await login(email, password);
      navigate('/', { replace: true });
    } catch (err) {
      toast.error('登录失败', { description: err instanceof Error ? err.message : '请检查邮箱与密码' });
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
        <form
          onSubmit={onSubmit}
          className="surface p-6 flex flex-col gap-4 shadow-soft"
        >
          <div>
            <div className="text-base font-semibold text-fg mb-1">登录</div>
            <div className="text-sm text-fg-muted">W1 demo · 任意邮箱密码均可通过</div>
          </div>
          <div className="flex flex-col gap-1.5">
            <label className="text-xs text-fg-muted" htmlFor="email">邮箱</label>
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
            <label className="text-xs text-fg-muted" htmlFor="password">密码</label>
            <Input
              id="password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="••••••••"
              autoComplete="current-password"
              required
            />
          </div>
          <Button type="submit" variant="primary" size="lg" loading={loading} className="mt-1">
            登录
          </Button>
        </form>
        <p className="mt-4 text-center text-xs text-fg-subtle">
          自部署版本 · 数据全在本地 · Apache-2.0
        </p>
      </div>
    </div>
  );
}
