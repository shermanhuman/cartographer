package project

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/fresh"
)

// View is what headers and status say about a registered project's map.
type View struct {
	Reg      *Registration
	State    *State
	Snapshot string
	MapID    string
	Source   string // main or head
	Created  time.Time
	Manifest *bundle.Manifest
	// Report is the checkouts' freshness (fresh.CheckWork) over the
	// snapshot's repos; Yours names the repo the command runs in ("" none).
	Report  *fresh.Report
	Yours   string
	Running time.Time // a cycle holds the lock since (zero none)
	Now     time.Time
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

func in(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d min", max(1, int(d.Minutes()+0.5)))
	}
	return fmt.Sprintf("%d h", int(d.Hours()+0.5))
}

// firstLine is a message's first line, with its last line too when it has
// several (a command's error, then the end of its stderr).
func firstLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(lines[0])
	if n := len(lines); n > 1 {
		if last := strings.TrimSpace(lines[n-1]); last != "" {
			s += " … " + last
		}
	}
	if len(s) > 200 {
		cut := 200
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// Label is the header's map name: 8 hex digits of the map id.
func (v *View) Label() string {
	if v.MapID == "" {
		return "map " + v.Snapshot
	}
	return "map " + v.MapID[:min(8, len(v.MapID))]
}

// fetched is when the mapped mains were last confirmed: the oldest of the
// members' last successful fetches.
func (v *View) fetched() time.Time {
	var oldest time.Time
	for _, r := range v.Reg.Members {
		f, ok := v.State.Fetch[r.Name()]
		if !ok || f.LastOK.IsZero() {
			continue
		}
		if oldest.IsZero() || f.LastOK.Before(oldest) {
			oldest = f.LastOK
		}
	}
	return oldest
}

// Lead is the header's line after the label.
func (v *View) Lead() string {
	var parts []string
	if v.Source == "head" {
		parts = append(parts, "your checkouts, built "+ago(v.Now.Sub(v.Created)))
	} else if t := v.fetched(); !t.IsZero() {
		parts = append(parts, "main as fetched "+ago(v.Now.Sub(t)))
	} else {
		parts = append(parts, "main")
	}
	if w := v.yours(); w != "" {
		parts = append(parts, "your checkout: "+w)
	}
	parts = append(parts, v.Problems()...)
	return strings.Join(parts, " · ")
}

func (v *View) state(name string) *fresh.State {
	if v.Report == nil {
		return nil
	}
	for i := range v.Report.Repos {
		if v.Report.Repos[i].Name == name {
			return &v.Report.Repos[i]
		}
	}
	return nil
}

// yours describes the checkout the command runs in.
func (v *View) yours() string {
	st := v.state(v.Yours)
	if st == nil {
		return ""
	}
	return work(st)
}

func work(st *fresh.State) string {
	var parts []string
	if st.Branch != "" {
		parts = append(parts, st.Branch)
	} else {
		parts = append(parts, "detached HEAD")
	}
	switch {
	case st.Ahead < 0 || st.BehindMap < 0:
		parts = append(parts, "ahead/behind the map unknown")
	default:
		if st.Ahead > 0 {
			parts = append(parts, fmt.Sprintf("%d ahead", st.Ahead))
		}
		if st.BehindMap > 0 {
			parts = append(parts, fmt.Sprintf("%d behind", st.BehindMap))
		}
	}
	if n := len(st.Dirty); n > 0 {
		parts = append(parts, fmt.Sprintf("%d uncommitted", n))
	}
	if n := len(st.Changed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d files differ from the map", n))
	}
	return strings.Join(parts, ", ")
}

// Problems are the reasons the map may be behind the fetched commits:
// a running rebuild, the last failure, fetch failures and missing
// checkouts.
func (v *View) Problems() []string {
	var out []string
	if !v.Running.IsZero() {
		out = append(out, "rebuilding since "+v.Running.Local().Format("15:04"))
	}
	if f := v.State.Failed; f != nil {
		what := f.Step
		if f.Repo != "" {
			what = RepoName(f.Repo) + " failed at " + f.Step
		}
		if f.Commit != "" {
			what += " at " + short(f.Commit)
		}
		retry := "retry at the next use after " + v.State.Attempt.Add(Cadence).Local().Format("15:04")
		if f.Digest != "" {
			if d := f.At.Add(FailedRetry).Sub(v.Now); d > 0 {
				retry = "retry in " + in(d)
			} else {
				retry = "retry at the next use"
			}
		}
		s := fmt.Sprintf("behind the fetched commits: %s: %s; %s or `cartographer refresh`", what, firstLine(f.Message), retry)
		if f.Log != "" {
			s += " (log: " + f.Log + ")"
		}
		out = append(out, s)
	}
	var names []string
	for n := range v.State.Fetch {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := v.State.Fetch[n]
		if f.Error == "" {
			continue
		}
		s := fmt.Sprintf("%s fetch failed %s: %s", n, ago(v.Now.Sub(f.At)), firstLine(f.Error))
		if f.Commit != "" {
			s += "; using its last fetched commit " + short(f.Commit)
		}
		out = append(out, s)
	}
	for _, m := range v.Reg.Members {
		if m.Path == "" {
			out = append(out, fmt.Sprintf("%s has no checkout: %s", m.Name(), CloneCommand(m.Repo, nil)))
		}
	}
	return out
}

// Detail is status's description: the map id and snapshot, each repo with
// its mapped commit and checkout, what the inputs were, and the problems.
func (v *View) Detail() []string {
	var out []string
	id := v.MapID
	if id == "" {
		id = "unknown"
	}
	out = append(out, fmt.Sprintf("project %s · map id %s (snapshot %s is the internal id)", v.Reg.Name, id, v.Snapshot))
	out = append(out, v.Lead())
	if v.Report != nil {
		for _, st := range v.Report.Repos {
			rev := short(st.Revision)
			line := fmt.Sprintf("%s@%s", st.Name, rev)
			if f, ok := v.State.Fetch[st.Name]; ok && f.Commit != "" && f.Commit != st.Revision && v.Source != "head" {
				line += fmt.Sprintf(" (fetched %s, not mapped yet)", short(f.Commit))
			}
			switch {
			case st.Note != "":
				line += ": " + st.Note
			default:
				line += fmt.Sprintf(": %s: %s", st.Path, work(&st))
			}
			if st.Name == v.Yours {
				line += " (you are here)"
			}
			out = append(out, line)
		}
	}
	if m := v.Manifest; m != nil {
		var tools []string
		for k, val := range m.Tools {
			tools = append(tools, k+"="+val)
		}
		sort.Strings(tools)
		d := m.InputsDigest
		if len(d) > 16 {
			d = d[:16]
		}
		line := "tools " + strings.Join(tools, " ")
		if d != "" {
			line = "inputs digest " + d + " · " + line
		}
		out = append(out, line)
	}
	return out
}
