import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { FileQuestion } from 'lucide-react';

export function NotFoundPage() {
  return (
    <div className="mx-auto max-w-3xl px-8 py-16">
      <EmptyState
        icon={<FileQuestion />}
        title="404 · 这里什么也没有"
        description="可能链接过期了，或者你需要先登录。"
        action={
          <Link to="/">
            <Button variant="secondary" size="sm">回到首页</Button>
          </Link>
        }
      />
    </div>
  );
}
