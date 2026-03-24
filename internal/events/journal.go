package events

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrCursorExpired = errors.New("sandbox event cursor expired")

type Event struct {
	EventID        string
	Sequence       int64
	Cursor         string
	SandboxID      string
	EventType      string
	OccurredAt     time.Time
	Replay         bool
	Snapshot       bool
	Phase          string
	DependencyName string
	ErrorCode      string
	ErrorMessage   string
	Reason         string
	ExecID         string
	ExitCode       *int
	Error          string
}

type Journal struct {
	mu           sync.RWMutex
	now          func() time.Time
	replayWindow time.Duration
	sequences    map[string]int64
	events       map[string][]Event
}

func NewJournal(replayWindow time.Duration, now func() time.Time) *Journal {
	resolvedNow := now
	if resolvedNow == nil {
		resolvedNow = time.Now
	}
	return &Journal{
		now:          resolvedNow,
		replayWindow: replayWindow,
		sequences:    make(map[string]int64),
		events:       make(map[string][]Event),
	}
}

func (journal *Journal) Append(event Event) Event {
	journal.mu.Lock()
	defer journal.mu.Unlock()

	journal.sequences[event.SandboxID]++
	sequence := journal.sequences[event.SandboxID]
	resolved := event
	resolved.Sequence = sequence
	resolved.OccurredAt = journal.now().UTC()
	resolved.EventID = fmt.Sprintf("%s-%06d", event.SandboxID, sequence)
	resolved.Cursor = resolved.EventID
	journal.events[event.SandboxID] = append(journal.events[event.SandboxID], resolved)
	journal.trimLocked(event.SandboxID)
	return resolved
}

func (journal *Journal) Replay(sandboxID string, fromCursor string) ([]Event, error) {
	journal.mu.RLock()
	defer journal.mu.RUnlock()

	entries := append([]Event(nil), journal.events[sandboxID]...)
	if fromCursor == "" {
		return markReplay(entries), nil
	}
	index := -1
	for candidateIndex, event := range entries {
		if event.Cursor == fromCursor {
			index = candidateIndex
			break
		}
	}
	if index == -1 {
		if len(entries) > 0 {
			oldest := entries[0].OccurredAt
			if journal.now().UTC().Sub(oldest) > journal.replayWindow {
				return nil, ErrCursorExpired
			}
		}
		return nil, ErrCursorExpired
	}
	return markReplay(entries[index+1:]), nil
}

func markReplay(entries []Event) []Event {
	for index := range entries {
		entries[index].Replay = true
	}
	return entries
}

func (journal *Journal) trimLocked(sandboxID string) {
	if journal.replayWindow <= 0 {
		return
	}
	threshold := journal.now().UTC().Add(-journal.replayWindow)
	filtered := journal.events[sandboxID][:0]
	for _, event := range journal.events[sandboxID] {
		if event.OccurredAt.Before(threshold) {
			continue
		}
		filtered = append(filtered, event)
	}
	journal.events[sandboxID] = filtered
}
