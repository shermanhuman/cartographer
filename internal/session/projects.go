package session

import (
	"context"
	"fmt"
	"time"

	"github.com/shermanhuman/cartographer/internal/project"
)

// ErrUnknownProject means no registered project, and no main build, has the name.
type ErrUnknownProject struct{ Name string }

func (e *ErrUnknownProject) Error() string { return fmt.Sprintf("no project %s", e.Name) }

// NotFound marks the error as the caller's to fix (the server answers 404).
func (e *ErrUnknownProject) NotFound() bool { return true }

// NotFound marks a project without a map yet as a 404 for the server.
func (e *ErrFirstBuild) NotFound() bool { return true }

// ProjectSnapshot returns the newest main build of project name: the only
// map a registered project shows (see chooseRepo). A hit reads the store
// alone, so it never waits behind a freshness check. On a miss it takes the
// session lock, starts any due cycle, and says why there's no map yet
// (*ErrFirstBuild), or that there is no such project (*ErrUnknownProject).
func (s *Session) ProjectSnapshot(ctx context.Context, name string) (string, error) {
	if si, ok, err := s.Store.Newest(ctx, name, "main"); err != nil || ok {
		return si.ID, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.current(ctx)
	s.scheduleProjects(m)
	reg := registered(m.projects, name)
	if reg == nil {
		return "", &ErrUnknownProject{Name: name}
	}
	return "", s.firstBuild(reg)
}

// ProjectInfo is one registered project as `serve` reports it.
type ProjectInfo struct {
	Name         string    `json:"name"`
	Anchor       string    `json:"anchor"`
	Members      []string  `json:"members"` // owner/name
	LastSnapshot string    `json:"last_snapshot"`
	LastCycle    CycleInfo `json:"last_cycle"`
}

// CycleInfo is a project's last build cycle: state is running, failed,
// built or never; at is when it started, failed or imported.
type CycleInfo struct {
	State string `json:"state"`
	At    string `json:"at,omitempty"`
	Error string `json:"error,omitempty"`
}

// ProjectList is the answer to GET /api/projects.
type ProjectList struct {
	Projects []ProjectInfo         `json:"projects"`
	Problems []project.ListProblem `json:"problems"`
}

// Projects lists the projects registered with this session's store. A
// registration that can't be read is a problem (its store is unknown, so it
// may belong to another store); a registry that can't be read is an error.
func (s *Session) Projects(ctx context.Context) (*ProjectList, error) {
	regs, problems, err := project.ListDetailed(s.StateDir)
	if err != nil {
		return nil, err
	}
	out := &ProjectList{Projects: []ProjectInfo{}, Problems: problems}
	if out.Problems == nil {
		out.Problems = []project.ListProblem{}
	}
	for _, reg := range regs {
		if !reg.InStore(s.DB) {
			continue
		}
		p := ProjectInfo{Name: reg.Name, Anchor: reg.Anchor, Members: []string{}}
		for _, m := range reg.Members {
			p.Members = append(p.Members, m.Repo)
		}
		si, ok, err := s.Store.Newest(ctx, reg.Name, "main")
		if err != nil {
			return nil, err
		}
		if ok {
			p.LastSnapshot = si.ID
		}
		p.LastCycle = s.cycle(reg.Name)
		out.Projects = append(out.Projects, p)
	}
	return out, nil
}

func (s *Session) cycle(name string) CycleInfo {
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	if since, running := project.Lock(s.StateDir, name).Running(name); running {
		return CycleInfo{State: "running", At: stamp(since)}
	}
	st, err := project.LoadState(s.StateDir, name)
	switch {
	case err != nil:
		return CycleInfo{State: "failed", Error: err.Error()}
	case st.Failed != nil:
		return CycleInfo{State: "failed", At: stamp(st.Failed.At), Error: st.Failed.Step + ": " + st.Failed.Message}
	case !st.ImportedAt.IsZero():
		return CycleInfo{State: "built", At: stamp(st.ImportedAt)}
	}
	return CycleInfo{State: "never"}
}
