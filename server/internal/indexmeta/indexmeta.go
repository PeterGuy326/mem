// Package indexmeta owns the minimum metadata needed to keep query and corpus
// vectors in the same model space.
package indexmeta

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var userLocks sync.Map // map[uuid.UUID]*sync.RWMutex

const LegacyUnknownProvider = "legacy:unknown"

var (
	ErrMixedProviders  = errors.New("text corpus contains multiple embedding providers")
	ErrUnknownProvider = errors.New("text corpus embedding provider is unknown")
)

func userLock(userID uuid.UUID) *sync.RWMutex {
	lock, _ := userLocks.LoadOrStore(userID, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

// LockIndexing allows concurrent index jobs for one user while excluding a
// provider switch for the duration of model resolution, processing, and
// persistence. The current Phase 1 server is a single process; versioned index
// generations will replace this coordinator for distributed deployments.
func LockIndexing(userID uuid.UUID) func() {
	lock := userLock(userID)
	lock.RLock()
	return lock.RUnlock
}

// LockProviderSwitch waits for in-flight index jobs and prevents new ones from
// starting until the provider choice is committed.
func LockProviderSwitch(userID uuid.UUID) func() {
	lock := userLock(userID)
	lock.Lock()
	return lock.Unlock
}

// TextProvider returns the single provider recorded by an existing user's
// text corpus. hasCorpus is false when the user has no text embeddings.
// Mixed or legacy-unknown corpora fail closed and must be rebuilt.
func TextProvider(
	ctx context.Context,
	pool *pgxpool.Pool,
	userID uuid.UUID,
) (spec string, hasCorpus bool, err error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT e.provider
		  FROM embeddings_text e
		  JOIN files f ON f.id = e.file_id
		 WHERE f.user_id = $1
		 LIMIT 2`, userID)
	if err != nil {
		return "", false, fmt.Errorf("query text corpus provider: %w", err)
	}
	defer rows.Close()

	providers := make([]string, 0, 2)
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return "", false, fmt.Errorf("scan text corpus provider: %w", err)
		}
		providers = append(providers, strings.TrimSpace(provider))
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(providers) == 0 {
		return "", false, nil
	}
	if len(providers) > 1 {
		return "", true, ErrMixedProviders
	}
	if providers[0] == "" || providers[0] == LegacyUnknownProvider {
		return "", true, ErrUnknownProvider
	}
	return providers[0], true, nil
}
