package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shermanhuman/cartographer/internal/workspace"
)

// Cadence is how old the last fetch attempt must be before use starts
// another cycle.
var Cadence = 15 * time.Minute

// FailedRetry is how long a failed inputs digest waits before an automatic
// retry (`cartographer refresh` retries at once, and a new commit gives a
// new digest).
var FailedRetry = 6 * time.Hour

// State is a project's state file: when it last tried to fetch and what
// each repo answered, what it last imported and what last failed.
type State struct {
	// Attempt is when the last cycle started (zero: never attempted).
	Attempt time.Time `json:"attempt,omitzero"`
	// Fetch is each repo's result in the last attempt that fetched it.
	Fetch map[string]FetchResult `json:"fetch,omitempty"`
	// Imported is the inputs digest last imported, and when.
	Imported   string    `json:"imported,omitempty"`
	ImportedAt time.Time `json:"imported_at,omitzero"`
	// Failed is the last failure since the last import (nil none).
	Failed *Failure `json:"failed,omitempty"`
}

// FetchResult is one repo's fetch outcome.
type FetchResult struct {
	At     time.Time `json:"at"`
	Branch string    `json:"branch,omitempty"`
	Error  string    `json:"error,omitempty"`
	// Commit is the last fetched commit (also on an error: the one in use).
	Commit string    `json:"commit,omitempty"`
	LastOK time.Time `json:"last_ok,omitzero"`
}

// Failure is a cycle that stopped before importing: the repo and step it
// stopped at, its message, the log, and the inputs digest it tried ("" when
// it stopped before one was known, e.g. a missing checkout).
type Failure struct {
	At      time.Time `json:"at"`
	Digest  string    `json:"digest,omitempty"`
	Repo    string    `json:"repo,omitempty"`
	Commit  string    `json:"commit,omitempty"`
	Step    string    `json:"step"`
	Message string    `json:"message"`
	Log     string    `json:"log,omitempty"`
}

func statePath(stateDir, name string) string {
	return filepath.Join(Dir(stateDir, name), "state.json")
}

// LoadState reads a project's state (empty when there is none yet).
func LoadState(stateDir, name string) (*State, error) {
	b, err := os.ReadFile(statePath(stateDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return &State{Fetch: map[string]FetchResult{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", statePath(stateDir, name), err)
	}
	if st.Fetch == nil {
		st.Fetch = map[string]FetchResult{}
	}
	return &st, nil
}

// SaveState writes it atomically.
func SaveState(stateDir, name string, st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return workspace.WriteAtomic(statePath(stateDir, name), append(b, '\n'))
}

// Due reports whether use should start a cycle: never attempted, or the
// last attempt is at least Cadence old.
func (st *State) Due(now time.Time) bool {
	return st.Attempt.IsZero() || now.Sub(st.Attempt) >= Cadence
}

// held reports whether digest failed less than FailedRetry ago.
func (st *State) held(digest string, now time.Time) bool {
	return st.Failed != nil && st.Failed.Digest != "" && st.Failed.Digest == digest && now.Sub(st.Failed.At) < FailedRetry
}
