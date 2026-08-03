/**
 * Global top nav: logo + drive/providers links + global search box + account
 * menu. Rendered by AppLayout for the simple pages, and directly by
 * ExplorerPage (which passes its breadcrumb in via `children`).
 */
import * as React from 'react';
import { useNavigate, Link, useLocation } from 'react-router-dom';
import {
  ArrowLeftRight,
  BookOpenText,
  LogOut,
  FolderOpen,
  Moon,
  ScrollText,
  Settings,
  Search,
  Sun,
} from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { Logo } from './Logo';
import { useAuth } from '@/hooks/useAuth';
import { useTheme } from '@/hooks/useTheme';
import { cn } from '@/lib/cn';
import { useT, LANGS } from '@/i18n';
import { useCapabilities } from '@/hooks/useWorkspace';
import * as DropdownMenu from '@radix-ui/react-dropdown-menu';

const baseNavItems = [
  { to: '/drive', labelKey: 'nav.drive', icon: FolderOpen, match: '/drive' },
  { to: '/providers', labelKey: 'nav.providers', icon: Settings, match: '/providers' },
];

export function TopBar({ children }: { children?: React.ReactNode }) {
  const navigate = useNavigate();
  const location = useLocation();
  const { user, logout } = useAuth();
  const { theme, toggleTheme } = useTheme();
  const { t, lang, setLang } = useT();
  const capabilities = useCapabilities();
  const [q, setQ] = React.useState('');

  const isActive = (prefix: string) => location.pathname.startsWith(prefix);
  const navItems = React.useMemo(() => {
    const items = [baseNavItems[0]!];
    if (capabilities.data?.permissions.read && capabilities.data.features.memory !== false) {
      items.push({
        to: '/memories',
        labelKey: 'nav.memories',
        icon: BookOpenText,
        match: '/memories',
      });
    }
    if (capabilities.data?.features?.handoff && capabilities.data.permissions.read) {
      items.push({
        to: '/tasks',
        labelKey: 'nav.tasks',
        icon: ScrollText,
        match: '/tasks',
      });
    }
    items.push({
      to: '/transfer',
      labelKey: 'nav.transfer',
      icon: ArrowLeftRight,
      match: '/transfer',
    });
    items.push(baseNavItems[1]!);
    return items;
  }, [capabilities.data]);

  return (
    <header className="sticky top-0 z-30 h-12 flex-none border-b border-border bg-bg/85 backdrop-blur-md">
      <div className="h-full px-2.5 sm:px-4 flex items-center gap-2 sm:gap-3">
        <Logo />
        <div className="h-5 w-px bg-border" aria-hidden />
        <nav className="flex min-w-0 items-center gap-0.5 sm:gap-1">
          {navItems.map((it) => {
            const Icon = it.icon;
            return (
              <Link
                key={it.to}
                to={it.to}
                aria-label={t(it.labelKey)}
                className={cn(
                  'inline-flex items-center gap-1.5 rounded-md px-2 lg:px-2.5 h-8 text-sm transition-colors',
                  isActive(it.match)
                    ? 'bg-bg-inset text-fg'
                    : 'text-fg-muted hover:text-fg hover:bg-bg-inset/60',
                )}
              >
                <Icon className="h-3.5 w-3.5" aria-hidden="true" />
                <span className="hidden lg:inline">{t(it.labelKey)}</span>
              </Link>
            );
          })}
        </nav>

        {children}

        <form
          className="ml-auto relative hidden md:block"
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
          <button
            type="button"
            onClick={toggleTheme}
            aria-label={theme === 'dark' ? t('nav.switchToLightTheme') : t('nav.switchToDarkTheme')}
            title={theme === 'dark' ? t('nav.switchToLightTheme') : t('nav.switchToDarkTheme')}
            data-testid="theme-toggle"
            className="grid h-8 w-8 place-items-center rounded-md text-fg-muted transition-colors
                       hover:bg-bg-inset hover:text-fg"
          >
            {theme === 'dark' ? (
              <Sun className="h-4 w-4" aria-hidden="true" />
            ) : (
              <Moon className="h-4 w-4" aria-hidden="true" />
            )}
          </button>
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
                    <DropdownMenu.Item
                      key={l.code}
                      onSelect={() => setLang(l.code)}
                      className={cn(
                        'grid h-7 flex-1 cursor-pointer place-items-center rounded px-2 text-xs outline-none transition-colors',
                        lang === l.code
                          ? 'bg-bg-inset text-fg'
                          : 'text-fg-muted hover:text-fg hover:bg-bg-inset/60',
                      )}
                    >
                      {l.label}
                    </DropdownMenu.Item>
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
