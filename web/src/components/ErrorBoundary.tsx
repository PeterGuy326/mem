/**
 * App-wide crash fallback. Without this, any render error white-screens the
 * whole app. Catches it and shows a calm branded page with a reload + a
 * "back to drive" escape hatch, plus the error text (collapsed) for debugging.
 */
import * as React from 'react';
import { Logo } from '@/components/layout/Logo';
import { Button } from '@/components/ui/Button';
import { RotateCw, Home } from 'lucide-react';
import { tt } from '@/i18n';

interface Props {
  children: React.ReactNode;
}
interface State {
  error: Error | null;
}

export class ErrorBoundary extends React.Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // Surface to the console for debugging; no telemetry by design (local app).
    console.error('[mem] render crash:', error, info.componentStack);
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="grid min-h-screen place-items-center bg-bg px-6 text-fg">
        <div className="flex max-w-md flex-col items-center text-center">
          <div className="mb-5 rounded-lg border border-border bg-bg-panel px-4 py-3 shadow-soft">
            <Logo />
          </div>
          <h1 className="text-lg font-semibold">{tt('errorBoundary.title')}</h1>
          <p className="mt-2 text-sm leading-relaxed text-fg-muted">
            {tt('errorBoundary.description')}
          </p>

          <div className="mt-6 flex items-center gap-2">
            <Button onClick={() => window.location.reload()}>
              <RotateCw className="h-4 w-4" /> {tt('errorBoundary.reload')}
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                window.location.href = '/drive';
              }}
            >
              <Home className="h-4 w-4" /> {tt('errorBoundary.drive')}
            </Button>
          </div>

          <details className="mt-6 w-full text-left">
            <summary className="cursor-pointer text-2xs uppercase tracking-wider text-fg-subtle hover:text-fg">
              {tt('errorBoundary.details')}
            </summary>
            <pre className="mt-2 max-h-40 overflow-auto rounded-md border border-border bg-bg-inset p-3 text-2xs text-fg-muted">
              {error.message}
              {error.stack ? `\n\n${error.stack}` : ''}
            </pre>
          </details>
        </div>
      </div>
    );
  }
}
