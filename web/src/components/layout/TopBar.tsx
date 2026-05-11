import { useNavigate } from 'react-router-dom';
import { Search, LogOut } from 'lucide-react';
import { Input } from '@/components/ui/Input';
import { Kbd } from '@/components/ui/Kbd';
import { Button } from '@/components/ui/Button';
import { useAuth } from '@/hooks/useAuth';
import * as DropdownMenu from '@radix-ui/react-dropdown-menu';

export function TopBar() {
  const navigate = useNavigate();
  const { user, logout } = useAuth();

  function onSearchSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = e.currentTarget;
    const data = new FormData(form);
    const q = String(data.get('q') ?? '').trim();
    if (q) navigate(`/search?q=${encodeURIComponent(q)}`);
  }

  return (
    <header className="sticky top-0 z-30 h-14 border-b border-border bg-bg/85 backdrop-blur-md">
      <div className="h-full px-6 flex items-center gap-6">
        <form onSubmit={onSearchSubmit} className="flex-1 max-w-2xl">
          <Input
            name="q"
            placeholder="搜索图片、文档、人物… 比如 “2012 年和小明在云南拍的照片”"
            leadingIcon={<Search />}
            trailing={<Kbd>⏎</Kbd>}
            autoComplete="off"
          />
        </form>
        <div className="flex items-center gap-2 ml-auto">
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <button
                className="flex items-center gap-2 rounded-md px-2 h-9 hover:bg-bg-inset transition-colors"
                aria-label="账户菜单"
              >
                <div className="h-7 w-7 rounded-md bg-accent/20 text-accent grid place-items-center text-xs font-semibold">
                  {(user?.email ?? 'M').slice(0, 1).toUpperCase()}
                </div>
                <span className="hidden lg:block text-sm text-fg-muted">
                  {user?.email ?? 'guest'}
                </span>
              </button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content
                align="end"
                sideOffset={6}
                className="z-50 min-w-44 rounded-md border border-border bg-bg-panel p-1 shadow-soft animate-fade-in"
              >
                <DropdownMenu.Label className="px-2 py-1.5 text-2xs uppercase tracking-wider text-fg-subtle">
                  {user?.email ?? '未登录'}
                </DropdownMenu.Label>
                <DropdownMenu.Separator className="my-1 h-px bg-border" />
                <DropdownMenu.Item asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="w-full justify-start"
                    onClick={() => navigate('/settings')}
                  >
                    设置
                  </Button>
                </DropdownMenu.Item>
                <DropdownMenu.Item asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="w-full justify-start text-danger hover:text-danger"
                    onClick={() => {
                      logout();
                      navigate('/login');
                    }}
                  >
                    <LogOut className="h-3.5 w-3.5" />
                    退出登录
                  </Button>
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
      </div>
    </header>
  );
}
