// Package queue is the Redis-backed async task queue, wired on top of
// hibiken/asynq.
//
// Why a queue at all (SPEC §5.1):
//   - fire-and-forget goroutines lose work on memd restart / crash
//   - retries with exponential backoff need durable state
//   - provider switching ("reindex everything") needs to enqueue thousands
//     of tasks without blocking the HTTP handler
//
// Phase 1.5 scope:
//   - One task type: TypeIndexFile (file_id, user_id)
//   - In-process consumer (runs as a goroutine inside memd) — no separate
//     worker binary yet. Promotion to a standalone process is a one-line
//     change since asynq.Server is decoupled from asynq.Client.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// Task type constants — keep stable; consumers match on these strings.
const (
	TypeIndexFile = "index_file"
)

// IndexFilePayload is the JSON body of a TypeIndexFile task.
type IndexFilePayload struct {
	FileID uuid.UUID `json:"file_id"`
	UserID uuid.UUID `json:"user_id"`
}

// Client is the producer side. Safe for concurrent use.
type Client struct {
	asyn *asynq.Client
	log  *slog.Logger
}

// NewClient constructs a Client connected to the given redis URL.
// Pass an empty redisURL to disable the queue entirely — Enqueue becomes a
// no-op that logs at debug level so callers don't crash in dev without redis.
func NewClient(redisURL string, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}
	if redisURL == "" {
		return &Client{asyn: nil, log: log}, nil
	}
	opt, err := parseRedis(redisURL)
	if err != nil {
		return nil, fmt.Errorf("queue: parse redis url: %w", err)
	}
	return &Client{asyn: asynq.NewClient(opt), log: log}, nil
}

// Close releases the underlying redis pool.
func (c *Client) Close() error {
	if c == nil || c.asyn == nil {
		return nil
	}
	return c.asyn.Close()
}

// Enabled reports whether the queue is wired to a real redis.
func (c *Client) Enabled() bool { return c != nil && c.asyn != nil }

// EnqueueIndexFile pushes a TypeIndexFile task. The task is unique-by-file so
// rapid re-uploads / re-touches collapse to one consumer run.
func (c *Client) EnqueueIndexFile(ctx context.Context, p IndexFilePayload) error {
	if !c.Enabled() {
		c.log.Debug("queue.disabled", "task", TypeIndexFile, "file_id", p.FileID)
		return nil
	}
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	task := asynq.NewTask(TypeIndexFile, body)
	_, err = c.asyn.EnqueueContext(ctx, task,
		asynq.MaxRetry(3),
		asynq.Timeout(10*time.Minute),
		asynq.Retention(24*time.Hour),
		// One in-flight indexing per file at a time — avoids duplicate work
		// when the upload handler retries.
		asynq.TaskID(fmt.Sprintf("index_file:%s", p.FileID)),
		asynq.Unique(5*time.Minute),
	)
	if err != nil {
		// Asynq returns ErrTaskIDConflict / ErrDuplicateTask — these are
		// fine: the file is already queued.
		if isDuplicate(err) {
			c.log.Debug("queue.duplicate_skip", "file_id", p.FileID)
			return nil
		}
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	// asynq surfaces a few different wordings depending on whether the
	// conflict is on TaskID, Unique constraint, or scheduled-set position.
	s := err.Error()
	return strings.Contains(s, "task ID already exists") ||
		strings.Contains(s, "task ID conflicts") ||
		strings.Contains(s, "duplicate task") ||
		strings.Contains(s, "already unique") ||
		strings.Contains(s, "ErrTaskIDConflict") ||
		strings.Contains(s, "ErrDuplicateTask")
}

// --- Consumer side ---

// IndexFileHandler is the dependency the consumer needs to actually index a
// file. Implemented by *indexer.Service in production — we keep this as an
// interface to break a potential import cycle and to let tests pass a stub.
type IndexFileHandler interface {
	IndexFileByID(ctx context.Context, fileID, userID uuid.UUID) error
}

// Server wraps an asynq.Server bound to an IndexFileHandler.
type Server struct {
	srv *asynq.Server
	mux *asynq.ServeMux
	log *slog.Logger
}

// NewServer constructs the consumer side. concurrency is the number of
// parallel handlers; 4 is a sane default for a single-machine dev box.
func NewServer(redisURL string, concurrency int, h IndexFileHandler, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if redisURL == "" {
		return nil, fmt.Errorf("queue server: empty redis url")
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	opt, err := parseRedis(redisURL)
	if err != nil {
		return nil, fmt.Errorf("queue: parse redis url: %w", err)
	}
	srv := asynq.NewServer(opt, asynq.Config{
		Concurrency: concurrency,
		Queues:      map[string]int{"default": 6, "low": 1},
		// LogLevel: asynq's own logger is noisy; keep it warn+.
		LogLevel: asynq.WarnLevel,
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, t *asynq.Task, e error) {
			log.Error("queue.task_error", "type", t.Type(), "err", e)
		}),
	})
	mux := asynq.NewServeMux()
	mux.HandleFunc(TypeIndexFile, indexFileHandler(h, log))
	return &Server{srv: srv, mux: mux, log: log}, nil
}

// Run blocks until ctx is cancelled. Designed to be called in its own
// goroutine from memd.run().
func (s *Server) Run(ctx context.Context) error {
	if err := s.srv.Start(s.mux); err != nil {
		return fmt.Errorf("queue start: %w", err)
	}
	<-ctx.Done()
	s.srv.Shutdown()
	return nil
}

func indexFileHandler(h IndexFileHandler, log *slog.Logger) asynq.HandlerFunc {
	return func(ctx context.Context, t *asynq.Task) error {
		var p IndexFilePayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			// Bad payloads should NOT be retried — asynq's SkipRetry flips
			// the task to dead immediately.
			return fmt.Errorf("%w: bad payload: %v", asynq.SkipRetry, err)
		}
		log.Info("queue.index_file.start", "file_id", p.FileID, "user_id", p.UserID)
		if err := h.IndexFileByID(ctx, p.FileID, p.UserID); err != nil {
			log.Error("queue.index_file.failed", "file_id", p.FileID, "err", err)
			return err
		}
		log.Info("queue.index_file.ok", "file_id", p.FileID)
		return nil
	}
}

// parseRedis accepts both redis://host:port and bare host:port; asynq itself
// requires a redis.UniversalClient option, so we hand it RedisClientOpt.
func parseRedis(u string) (asynq.RedisConnOpt, error) {
	u = strings.TrimSpace(u)
	if u == "" {
		return nil, fmt.Errorf("empty redis url")
	}
	if strings.HasPrefix(u, "redis://") {
		// asynq has a built-in parser for the URL form.
		opt, err := asynq.ParseRedisURI(u)
		if err != nil {
			return nil, err
		}
		return opt, nil
	}
	return asynq.RedisClientOpt{Addr: u}, nil
}
