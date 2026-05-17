/**
 * Top nav: drive / ask / faces / providers links + account menu.
 */
import { useNavigate, Link, useLocation } from 'react-router-dom';
import { LogOut, FolderOpen, Sparkles, Users, Settings } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { useAuth } from '@/hooks/useAuth';
import { cn } from '@/lib/cn';
import * as DropdownMenu from '@radix-ui/react-dropdown-menu';

export function TopBar() {
  const navigate = useNavigate();
  const location = useLocation();
  const { user, logout } = useAuth();

  const isActive = (prefix: string) => location.pathname.startsWith(prefix);
  const navItems = [
    { to: '/drive', label: 'Drive', icon: FolderOpen, match: '/drive' },
    { to: '/ask', label: 'Ask', icon: Sparkles, match: '/ask' },
    { to: '/faces', label: 'Faces', icon: Users, match: '/faces' },
    { to: '/providers', label: 'Providers', icon: Settings, match: '/providers' },
  ];

  return (
    <header className="sticky top-0 z-30 h-12 border-b border-border bg-bg/85 backdrop-blur-md">
      <div className="h-full px-4 flex items-center gap-3">
        <nav className="flex items-center gap-1">
          {navItems.map((it) => {
            const Icon = it.icon;
            return (
              <Link
                key={it.to}
                to={it.to}
                className={cn(
                  'inline-flex items-center gap-1.5 rounded-md px-2.5 h-8 text-sm transition-colors',
                  isActive(it.match)
                    ? 'bg-bg-inset text-fg'
                    : 'text-fg-muted hover:text-fg hover:bg-bg-inset/60',
                )}
              >
                <Icon className="h-3.5 w-3.5" />
                {it.label}
              </Link>
            );
          })}
        </nav>
        <div className="ml-auto flex items-center gap-2">
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <button
                className="flex items-center gap-2 rounded-md px-2 h-8 hover:bg-bg-inset transition-colors"
                aria-label="账户菜单"
              >
                <div className="h-6 w-6 rounded-md bg-accent/20 text-accent grid place-items-center text-xs font-semibold">
                  {(user?.email ?? 'M').slice(0, 1).toUpperCase()}
                </div>
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
