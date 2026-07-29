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

func TestFileIndexingRunsAreSerializedPerFile(t *testing.T) {
	fileID := uuid.New()
	isLatest, unlockFirst := LockFileIndexing(fileID)
	if !isLatest {
		t.Fatal("first index run was unexpectedly superseded")
	}

	type acquisition struct {
		isLatest bool
		unlock   func()
	}
	acquired := make(chan acquisition, 2)
	for range 2 {
		go func() {
			current, unlock := LockFileIndexing(fileID)
			acquired <- acquisition{isLatest: current, unlock: unlock}
		}()
	}
	waitForFileLockReferences(t, fileID, 3)
	unlockFirst()

	var latest, superseded int
	for range 2 {
		select {
		case result := <-acquired:
			if result.isLatest {
				latest++
			} else {
				superseded++
			}
			result.unlock()
		case <-time.After(time.Second):
			t.Fatal("waiting index run did not acquire after the first completed")
		}
	}
	if latest != 1 || superseded != 1 {
		t.Fatalf("latest=%d superseded=%d, want 1/1", latest, superseded)
	}

	fileLocksM.Lock()
	_, exists := fileLocks[fileID]
	fileLocksM.Unlock()
	if exists {
		t.Fatal("unused file lock was not reclaimed")
	}
}

func waitForFileLockReferences(t *testing.T, fileID uuid.UUID, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fileLocksM.Lock()
		lock := fileLocks[fileID]
		references := 0
		if lock != nil {
			references = lock.references
		}
		fileLocksM.Unlock()
		if references == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("file lock references did not reach %d", want)
}
