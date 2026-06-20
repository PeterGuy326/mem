import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { FileQuestion } from 'lucide-react';
import { useT } from '@/i18n';

export function NotFoundPage() {
  const { t } = useT();
  return (
    <div className="mx-auto max-w-3xl px-8 py-16">
      <EmptyState
        icon={<FileQuestion />}
        title={t('notFound.title')}
        description={t('notFound.desc')}
        action={
          <Link to="/">
            <Button variant="secondary" size="sm">{t('action.home')}</Button>
          </Link>
        }
      />
    </div>
  );
}
