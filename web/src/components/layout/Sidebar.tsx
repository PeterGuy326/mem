import { NavLink } from 'react-router-dom';
import { Upload, Search, Settings, Users, Clock, Tag } from 'lucide-react';
import { Logo } from './Logo';
import { cn } from '@/lib/cn';

interface NavItem {
  to: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  end?: boolean;
  disabled?: boolean;
}

const PRIMARY: NavItem[] = [
  { to: '/', label: '上传', icon: Upload, end: true },
  { to: '/search', label: '搜索', icon: Search },
];

const SECONDARY: NavItem[] = [
  { to: '/timeline', label: '时间轴', icon: Clock, disabled: true },
  { to: '/faces', label: '人脸', icon: Users, disabled: true },
  { to: '/tags', label: '标签', icon: Tag, disabled: true },
];

export function Sidebar() {
  return (
    <aside className="hidden md:flex h-screen w-56 flex-col border-r border-border bg-bg-subtle">
      <div className="px-4 h-14 flex items-center border-b border-border">
        <Logo />
      </div>
      <nav className="flex-1 overflow-y-auto py-4 px-2 flex flex-col gap-6">
        <NavGroup label="主要">
          {PRIMARY.map((item) => (
            <SideLink key={item.to} item={item} />
          ))}
        </NavGroup>
        <NavGroup label="即将上线">
          {SECONDARY.map((item) => (
            <SideLink key={item.to} item={item} />
          ))}
        </NavGroup>
      </nav>
      <div className="border-t border-border p-3">
        <NavLink
          to="/settings"
          className={({ isActive }) =>
            cn(
              'flex items-center gap-2 rounded-md px-2.5 py-1.5 text-sm transition-colors',
              isActive ? 'bg-bg-inset text-fg' : 'text-fg-muted hover:bg-bg-inset hover:text-fg',
            )
          }
        >
          <Settings className="h-4 w-4" />
          设置
        </NavLink>
      </div>
    </aside>
  );
}

function NavGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="px-2.5 mb-1 text-2xs uppercase tracking-wider text-fg-subtle font-medium">
        {label}
      </div>
      <div className="flex flex-col gap-0.5">{children}</div>
    </div>
  );
}

function SideLink({ item }: { item: NavItem }) {
  if (item.disabled) {
    return (
      <span
        className="flex items-center gap-2 rounded-md px-2.5 py-1.5 text-sm text-fg-subtle cursor-not-allowed"
        title="Phase 2 / W2 上线"
      >
        <item.icon className="h-4 w-4" />
        <span className="flex-1">{item.label}</span>
        <span className="text-2xs text-fg-subtle">soon</span>
      </span>
    );
  }
  return (
    <NavLink
      to={item.to}
      end={item.end}
      className={({ isActive }) =>
        cn(
          'flex items-center gap-2 rounded-md px-2.5 py-1.5 text-sm transition-colors',
          isActive
            ? 'bg-accent/10 text-accent'
            : 'text-fg-muted hover:bg-bg-inset hover:text-fg',
        )
      }
    >
      <item.icon className="h-4 w-4" />
      {item.label}
    </NavLink>
  );
}
