import * as React from 'react';
import { Link } from 'react-router-dom';
import { Upload, FileText, Image as ImageIcon, Music, FileQuestion, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/cn';
import { Button } from '@/components/ui/Button';
import { StatusBadge } from '@/components/ui/Badge';
import { Skeleton } from '@/components/ui/Skeleton';
import { useRecentFiles, useUpload } from '@/hooks/useFiles';
import { formatBytes, formatRelative, truncateMiddle } from '@/lib/format';
import { toast } from 'sonner';
import type { FileKind, MemFile } from '@/lib/types';

export function UploadPage() {
  const upload = useUpload();
  const { data, isLoading } = useRecentFiles(10);
  const [dragOver, setDragOver] = React.useState(false);
  const inputRef = React.useRef<HTMLInputElement>(null);

  const handleFiles = React.useCallback(
    async (fileList: FileList | File[]) => {
      const files = Array.from(fileList);
      if (!files.length) return;
      const t = toast.loading(`上传 ${files.length} 个文件…`);
      try {
        await upload.mutateAsync(files);
        toast.success(`已入库 ${files.length} 个文件`, {
          id: t,
          description: 'AI 索引会在后台异步处理，刷新查看状态变化',
        });
      } catch (err) {
        toast.error('上传失败', {
          id: t,
          description: err instanceof Error ? err.message : '请稍后重试',
        });
      }
    },
    [upload],
  );

  return (
    <div className="mx-auto max-w-5xl px-8 py-10">
      {/* Hero */}
      <section className="mb-8">
        <h1 className="text-3xl font-semibold tracking-tight text-balance">
          把你的记忆扔进来。
        </h1>
        <p className="mt-2 text-sm text-fg-muted leading-relaxed max-w-xl text-balance">
          照片、文档、录音、PDF — 一股脑往里扔。AI 会自动打标、抽实体、建关联，让你用自然语言一秒找回。
        </p>
      </section>

      {/* Drop zone */}
      <section
        onDragOver={(e) => {
          e.preventDefault();
          setDragOver(true);
        }}
        onDragLeave={() => setDragOver(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragOver(false);
          if (e.dataTransfer.files.length) void handleFiles(e.dataTransfer.files);
        }}
        onClick={() => inputRef.current?.click()}
        className={cn(
          'relative cursor-pointer rounded-xl border-2 border-dashed transition-all',
          'flex flex-col items-center justify-center text-center px-8 py-16',
          'bg-bg-subtle/40 hover:bg-bg-subtle',
          dragOver
            ? 'border-accent bg-accent/5 shadow-glow scale-[1.005]'
            : 'border-border hover:border-border-strong',
        )}
      >
        <div
          className={cn(
            'h-12 w-12 rounded-xl grid place-items-center mb-4 transition-colors',
            dragOver ? 'bg-accent/15 text-accent' : 'bg-bg-inset text-fg-muted',
          )}
        >
          <Upload className="h-6 w-6" />
        </div>
        <div className="text-base font-medium text-fg">
          {dragOver ? '松手即上传' : '拖拽文件到此处，或点击选择'}
        </div>
        <div className="mt-1.5 text-sm text-fg-muted">
          支持图片 / 文档 / PDF / 音频 · 单文件 ≤ 100MB · 自动去重
        </div>
        <Button variant="secondary" size="sm" className="mt-5 pointer-events-none">
          选择文件
        </Button>
        <input
          ref={inputRef}
          type="file"
          multiple
          hidden
          onChange={(e) => {
            if (e.target.files) void handleFiles(e.target.files);
            e.target.value = '';
          }}
        />
      </section>

      {/* Recent */}
      <section className="mt-10">
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold text-fg uppercase tracking-wider">最近入库</h2>
          <Link
            to="/search"
            className="text-xs text-fg-muted hover:text-fg inline-flex items-center gap-0.5"
          >
            浏览全部 <ChevronRight className="h-3 w-3" />
          </Link>
        </div>

        <div className="surface divide-y divide-border overflow-hidden">
          {isLoading
            ? Array.from({ length: 6 }).map((_, i) => <RecentRowSkeleton key={i} />)
            : data?.items.map((f) => <RecentRow key={f.id} file={f} />)}
          {!isLoading && data?.items.length === 0 && (
            <div className="py-12 text-center text-sm text-fg-muted">还没有文件，从上面拖一些进来 ↑</div>
          )}
        </div>
      </section>
    </div>
  );
}

function kindIcon(kind: FileKind) {
  switch (kind) {
    case 'image':
      return ImageIcon;
    case 'audio':
      return Music;
    case 'pdf':
    case 'doc':
    case 'text':
      return FileText;
    default:
      return FileQuestion;
  }
}

function RecentRow({ file }: { file: MemFile }) {
  const Icon = kindIcon(file.kind);
  return (
    <Link
      to={`/files/${file.id}`}
      className="group flex items-center gap-4 px-4 py-3 hover:bg-bg-inset/60 transition-colors"
    >
      <div className="h-10 w-10 flex-none rounded-md overflow-hidden bg-bg-inset border border-border grid place-items-center">
        {file.thumbnail_url ? (
          <img
            src={file.thumbnail_url}
            alt=""
            className="h-full w-full object-cover"
            loading="lazy"
          />
        ) : (
          <Icon className="h-4 w-4 text-fg-subtle" />
        )}
      </div>
      <div className="flex-1 min-w-0">
        <div className="text-sm text-fg truncate group-hover:text-accent transition-colors">
          {truncateMiddle(file.name, 56)}
        </div>
        <div className="text-2xs text-fg-subtle mt-0.5 flex items-center gap-2">
          <span>{formatBytes(file.size)}</span>
          <span aria-hidden="true">·</span>
          <span>{formatRelative(file.created_at)}</span>
        </div>
      </div>
      <StatusBadge status={file.index_status} />
      <ChevronRight className="h-4 w-4 text-fg-subtle opacity-0 group-hover:opacity-100 transition-opacity" />
    </Link>
  );
}

function RecentRowSkeleton() {
  return (
    <div className="flex items-center gap-4 px-4 py-3">
      <Skeleton className="h-10 w-10" />
      <div className="flex-1 space-y-2">
        <Skeleton className="h-3.5 w-1/2" />
        <Skeleton className="h-2.5 w-1/4" />
      </div>
      <Skeleton className="h-5 w-16" />
    </div>
  );
}
