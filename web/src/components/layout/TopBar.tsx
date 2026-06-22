/**
 * Global top nav: logo + drive/ask/faces/providers links + global search box
 * + account menu. Rendered by AppLayout for the simple pages, and directly by
 * ExplorerPage (which passes its breadcrumb in via `children`).
 */
import * as React from 'react';
import { useNavigate, Link, useLocation } from 'react-router-dom';
import { LogOut, FolderOpen, Settings, Search } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Logo } from './Logo';
import { useAuth } from '@/hooks/useAuth';
import { cn } from '@/lib/cn';
import { useT, LANGS } from '@/i18n';
import * as DropdownMenu from '@radix-ui/react-dropdown-menu';

// Ask is no longer a page — it's the floating assistant bubble (AskWidget),
// reachable from anywhere — so it's intentionally absent from the top nav.
const navItems = [
  { to: '/drive', labelKey: 'nav.drive', icon: FolderOpen, match: '/drive' },
  { to: '/providers', labelKey: 'nav.providers', icon: Settings, match: '/providers' },
];

export function TopBar({ children }: { children?: React.ReactNode }) {
  const navigate = useNavigate();
  const location = useLocation();
  const { user, logout } = useAuth();
  const { t, lang, setLang } = useT();
  const [q, setQ] = React.useState('');

  const isActive = (prefix: string) => location.pathname.startsWith(prefix);

  return (
    <header className="sticky top-0 z-30 h-12 flex-none border-b border-border bg-bg/85 backdrop-blur-md">
      <div className="h-full px-4 flex items-center gap-3">
        <Logo />
        <div className="h-5 w-px bg-border" aria-hidden />
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
                {t(it.labelKey)}
              </Link>
            );
          })}
        </nav>

        {children}

        <form
          className="ml-auto relative"
          onSubmit={(e) => {
            e.preventDefault();
            const v = q.trim();
            navigate(v ? `/search?q=${encodeURIComponent(v)}` : '/search');
          }}
        >
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-fg-subtle" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={t('nav.searchPlaceholder')}
            aria-label={t('search.title')}
            className="h-8 w-56 rounded-md border border-border bg-bg-inset pl-8 pr-3 text-sm
                       text-fg placeholder:text-fg-subtle outline-none focus:border-accent/60"
          />
        </form>

        <div className="flex items-center gap-2">
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <button
                className="flex items-center gap-2 rounded-md px-2 h-8 hover:bg-bg-inset transition-colors"
                aria-label={t('nav.account')}
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
                  {user?.email ?? t('nav.notSignedIn')}
                </DropdownMenu.Label>
                <DropdownMenu.Separator className="my-1 h-px bg-border" />
                <DropdownMenu.Label className="px-2 pt-1 pb-0.5 text-2xs uppercase tracking-wider text-fg-subtle">
                  {t('nav.language')}
                </DropdownMenu.Label>
                <div className="flex gap-1 px-1.5 pb-1">
                  {LANGS.map((l) => (
                    <button
                      key={l.code}
                      onClick={() => setLang(l.code)}
                      className={cn(
                        'flex-1 rounded px-2 h-7 text-xs transition-colors',
                        lang === l.code
                          ? 'bg-bg-inset text-fg'
                          : 'text-fg-muted hover:text-fg hover:bg-bg-inset/60',
                      )}
                    >
                      {l.label}
                    </button>
                  ))}
                </div>
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
                    {t('nav.logout')}
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
