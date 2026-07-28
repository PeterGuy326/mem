// Package pglockwait provides deterministic PostgreSQL lock barriers for
// integration tests. It is intended to be imported only by _test.go files.
package pglockwait

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const lockWaitTimeout = 5 * time.Second

// Backend is a single-connection pool whose PostgreSQL backend can be observed
// unambiguously through pg_stat_activity.
type Backend struct {
	Pool            *pgxpool.Pool
	PID             int32
	ApplicationName string
}

// NewBackend creates a tagged single-connection pool and records its backend
// PID. MaxConns=1 guarantees that a service using Pool runs on the recorded
// backend for the lifetime of this short integration test.
func NewBackend(t testing.TB, ctx context.Context, dsn, role string) Backend {
	t.Helper()

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse observed PostgreSQL pool config: %v", err)
	}
	config.MaxConns = 1
	config.MinConns = 1
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string)
	}
	applicationName := observedApplicationName(role)
	config.ConnConfig.RuntimeParams["application_name"] = applicationName

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create observed PostgreSQL pool %q: %v", applicationName, err)
	}
	t.Cleanup(pool.Close)

	pid := BackendPID(t, ctx, pool)
	return Backend{
		Pool:            pool,
		PID:             pid,
		ApplicationName: applicationName,
	}
}

// BackendPID returns the PostgreSQL backend PID used by a pool or transaction.
func BackendPID(t testing.TB, ctx context.Context, queryer rowQueryer) int32 {
	t.Helper()

	var pid int32
	if err := queryer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("load PostgreSQL backend PID: %v", err)
	}
	return pid
}

// WaitBlocked waits until waiter is actively blocked in a lock-taking SQL
// statement whose normalized text contains every queryFragment. The blocker
// may be direct or reached through PostgreSQL's soft lock-wait queue.
func WaitBlocked(
	t testing.TB,
	ctx context.Context,
	observer *pgxpool.Pool,
	waiter Backend,
	rootBlockerPID int32,
	queryFragments ...string,
) {
	t.Helper()

	deadline := time.Now().Add(lockWaitTimeout)
	var last activity
	for time.Now().Before(deadline) {
		current, err := loadActivity(ctx, observer, waiter.PID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf(
					"observe PostgreSQL backend %d (%s): %v",
					waiter.PID,
					waiter.ApplicationName,
					err,
				)
			}
		} else {
			last = current
			if current.State == "active" &&
				current.WaitEventType == "Lock" &&
				containsSQLFragments(current.Query, queryFragments) {
				reaches, chain, err := blockerChain(
					ctx,
					observer,
					waiter.PID,
					rootBlockerPID,
				)
				if err != nil {
					t.Fatalf(
						"inspect PostgreSQL blocker chain for backend %d (%s): %v",
						waiter.PID,
						waiter.ApplicationName,
						err,
					)
				}
				last.BlockerChain = chain
				if reaches {
					return
				}
			}
		}

		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			t.Fatalf(
				"context ended while waiting for PostgreSQL backend %d (%s): %v; last activity: %s",
				waiter.PID,
				waiter.ApplicationName,
				ctx.Err(),
				last,
			)
		case <-timer.C:
		}
	}

	t.Fatalf(
		"PostgreSQL backend %d (%s) did not block on query fragments %q through blocker %d within %s; last activity: %s",
		waiter.PID,
		waiter.ApplicationName,
		queryFragments,
		rootBlockerPID,
		lockWaitTimeout,
		last,
	)
}

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type activity struct {
	State          string
	WaitEventType  string
	WaitEvent      string
	Query          string
	DirectBlockers []int32
	BlockerChain   []int32
}

func (a activity) String() string {
	return fmt.Sprintf(
		"state=%q wait_event_type=%q wait_event=%q direct_blockers=%v blocker_chain=%v query=%q",
		a.State,
		a.WaitEventType,
		a.WaitEvent,
		a.DirectBlockers,
		a.BlockerChain,
		compactSQL(a.Query),
	)
}

func loadActivity(
	ctx context.Context,
	observer *pgxpool.Pool,
	pid int32,
) (activity, error) {
	var current activity
	err := observer.QueryRow(ctx, `
		SELECT COALESCE(state, ''),
		       COALESCE(wait_event_type, ''),
		       COALESCE(wait_event, ''),
		       COALESCE(query, ''),
		       pg_blocking_pids(pid)
		  FROM pg_stat_activity
		 WHERE datname = current_database()
		   AND pid = $1
	`, pid).Scan(
		&current.State,
		&current.WaitEventType,
		&current.WaitEvent,
		&current.Query,
		&current.DirectBlockers,
	)
	return current, err
}

func blockerChain(
	ctx context.Context,
	observer *pgxpool.Pool,
	waiterPID, rootBlockerPID int32,
) (bool, []int32, error) {
	queue := []int32{waiterPID}
	seen := map[int32]struct{}{waiterPID: {}}
	chain := make([]int32, 0, 4)

	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]

		var blockers []int32
		if err := observer.QueryRow(
			ctx,
			`SELECT pg_blocking_pids($1)`,
			pid,
		).Scan(&blockers); err != nil {
			return false, chain, err
		}
		for _, blockerPID := range blockers {
			chain = append(chain, blockerPID)
			if blockerPID == rootBlockerPID {
				return true, chain, nil
			}
			if _, ok := seen[blockerPID]; ok {
				continue
			}
			seen[blockerPID] = struct{}{}
			queue = append(queue, blockerPID)
		}
	}
	return false, chain, nil
}

func containsSQLFragments(query string, fragments []string) bool {
	normalized := compactSQL(query)
	for _, fragment := range fragments {
		if !strings.Contains(normalized, compactSQL(fragment)) {
			return false
		}
	}
	return true
}

func compactSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func observedApplicationName(role string) string {
	role = strings.Trim(strings.Join(strings.Fields(role), "-"), "-")
	if len(role) > 36 {
		role = role[:36]
	}
	if role == "" {
		role = "backend"
	}
	return "mem-lock-" + role + "-" + uuid.NewString()[:8]
}
