import * as React from 'react';

export type Theme = 'dark' | 'light';

export const THEME_STORAGE_KEY = 'mem.theme';

const THEME_COLORS: Record<Theme, string> = {
  dark: '#0a0b0f',
  light: '#fafafc',
};

type ThemeContextValue = {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  toggleTheme: () => void;
};

const ThemeContext = React.createContext<ThemeContextValue | null>(null);

function themeFromDocument(): Theme {
  return document.documentElement.classList.contains('light') ? 'light' : 'dark';
}

function applyDocumentTheme(theme: Theme): void {
  const root = document.documentElement;
  root.classList.remove('dark', 'light');
  root.classList.add(theme);
  root.dataset.theme = theme;
  root.style.colorScheme = theme;
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', THEME_COLORS[theme]);
}

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setThemeState] = React.useState<Theme>(themeFromDocument);

  const setTheme = React.useCallback((nextTheme: Theme) => {
    applyDocumentTheme(nextTheme);
    try {
      localStorage.setItem(THEME_STORAGE_KEY, nextTheme);
    } catch {
      // The active theme still works when storage is unavailable.
    }
    setThemeState(nextTheme);
  }, []);

  const toggleTheme = React.useCallback(() => {
    setTheme(theme === 'dark' ? 'light' : 'dark');
  }, [setTheme, theme]);

  React.useEffect(() => {
    const syncTheme = (event: StorageEvent) => {
      if (event.key !== THEME_STORAGE_KEY) return;
      const nextTheme: Theme = event.newValue === 'light' ? 'light' : 'dark';
      applyDocumentTheme(nextTheme);
      setThemeState(nextTheme);
    };
    window.addEventListener('storage', syncTheme);
    return () => window.removeEventListener('storage', syncTheme);
  }, []);

  const value = React.useMemo(
    () => ({ theme, setTheme, toggleTheme }),
    [setTheme, theme, toggleTheme],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeContextValue {
  const context = React.useContext(ThemeContext);
  if (!context) throw new Error('useTheme must be used within <ThemeProvider>');
  return context;
}
