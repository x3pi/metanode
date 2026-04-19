package transfer

import (
	"sync"
	"testing"
)

func TestNewStateManager(t *testing.T) {
	sm := NewStateManager()
	if sm == nil {
		t.Fatal("NewStateManager returned nil")
	}
	if sm.sessions == nil {
		t.Fatal("sessions map should be initialized")
	}
}

func TestStateManager_StartSession(t *testing.T) {
	sm := NewStateManager()
	sm.StartSession("archive1", 5)

	sm.mu.RLock()
	session, exists := sm.sessions["archive1"]
	sm.mu.RUnlock()

	if !exists {
		t.Fatal("session should exist after StartSession")
	}
	if session.BaseArchiveName != "archive1" {
		t.Errorf("BaseArchiveName: got %q, want %q", session.BaseArchiveName, "archive1")
	}
	if session.TotalChunks != 5 {
		t.Errorf("TotalChunks: got %d, want %d", session.TotalChunks, 5)
	}
	if session.ReceivedChunks != 0 {
		t.Errorf("ReceivedChunks: got %d, want %d", session.ReceivedChunks, 0)
	}
}

func TestStateManager_IncrementChunkCount(t *testing.T) {
	sm := NewStateManager()
	sm.StartSession("archive1", 3)

	// Increment 1st chunk — not complete
	if done := sm.IncrementChunkCount("archive1"); done {
		t.Error("should not be done after 1st chunk")
	}

	// Increment 2nd chunk — not complete
	if done := sm.IncrementChunkCount("archive1"); done {
		t.Error("should not be done after 2nd chunk")
	}

	// Increment 3rd chunk — complete!
	if done := sm.IncrementChunkCount("archive1"); !done {
		t.Error("should be done after 3rd chunk")
	}
}

func TestStateManager_IncrementChunkCount_NonExistent(t *testing.T) {
	sm := NewStateManager()
	if done := sm.IncrementChunkCount("no_such_session"); done {
		t.Error("should return false for non-existent session")
	}
}

func TestStateManager_EndSession(t *testing.T) {
	sm := NewStateManager()
	sm.StartSession("archive1", 3)
	sm.EndSession("archive1")

	sm.mu.RLock()
	_, exists := sm.sessions["archive1"]
	sm.mu.RUnlock()

	if exists {
		t.Error("session should be removed after EndSession")
	}
}

func TestStateManager_EndSession_NonExistent(t *testing.T) {
	sm := NewStateManager()
	// Should not panic
	sm.EndSession("no_such_session")
}

func TestStateManager_MultipleSessions(t *testing.T) {
	sm := NewStateManager()
	sm.StartSession("archive1", 2)
	sm.StartSession("archive2", 3)

	sm.IncrementChunkCount("archive1")
	if done := sm.IncrementChunkCount("archive1"); !done {
		t.Error("archive1 should be done after 2 chunks")
	}

	// archive2 should still be in progress
	sm.IncrementChunkCount("archive2")
	if done := sm.IncrementChunkCount("archive2"); done {
		t.Error("archive2 should not be done after 2/3 chunks")
	}
}

func TestStateManager_ConcurrentAccess(t *testing.T) {
	sm := NewStateManager()
	sm.StartSession("concurrent", 100)

	var wg sync.WaitGroup
	completions := make(chan bool, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done := sm.IncrementChunkCount("concurrent")
			completions <- done
		}()
	}

	wg.Wait()
	close(completions)

	doneCount := 0
	for done := range completions {
		if done {
			doneCount++
		}
	}

	if doneCount != 1 {
		t.Errorf("exactly one goroutine should see completion, got %d", doneCount)
	}
}
