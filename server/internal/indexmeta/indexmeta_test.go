package indexmeta

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestProviderSwitchWaitsForInFlightIndexing(t *testing.T) {
	userID := uuid.New()
	unlockIndex := LockIndexing(userID)
	acquired := make(chan func(), 1)
	go func() {
		acquired <- LockProviderSwitch(userID)
	}()

	select {
	case unlock := <-acquired:
		unlock()
		t.Fatal("exclusive provider lock acquired while indexing lock was held")
	case <-time.After(25 * time.Millisecond):
	}

	unlockIndex()
	select {
	case unlock := <-acquired:
		unlock()
	case <-time.After(time.Second):
		t.Fatal("provider lock did not acquire after indexing completed")
	}
}
