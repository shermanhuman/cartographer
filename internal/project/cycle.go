package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shermanhuman/cartographer/internal/fresh"
	"github.com/shermanhuman/cartographer/internal/rebuild"
	"github.com/shermanhuman/cartographer/internal/store"
	"github.com/shermanhuman/cartographer/internal/workspace"
)

// Runner runs a project's cycles: fetch every member, freeze the commits,
// and build and import when the inputs changed.
type Runner struct {
	StateDir string
	// Store is where maps are imported; DB is its file (for messages).
	Store *store.Store
	DB    string
	// PluginsDir holds the bundled plugins; PluginsDigest identifies them.
	PluginsDir, PluginsDigest string
	CacheDir                  string
	// Self is the cartographer executable (`extract scip`).
	Self string
	// Version is the cartographer version (inputs digest).
	Version string
	Mise    *Mise
	// Checkouts and Roots locate members (see Locator).
	Checkouts map[string]string
	Roots     []string
	// Now is the clock (default time.Now).
	Now      func() time.Time
	SchemaDB workspace.SchemaDB
	// BuildTimeout bounds the build after the fetches (default
	// rebuild.Timeout).
	BuildTimeout time.Duration
	// Log receives progress lines.
	Log io.Writer
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) logf(format string, a ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, format+"\n", a...)
	}
}

// Result is what one cycle did.
type Result struct {
	Imported bool
	Snapshot string
	// Skipped says why nothing was built ("" when something was).
	Skipped string
}

// Cycle runs one cycle of project name under its lock (rebuild.ErrRunning
// when another holds it). force is `cartographer refresh`: it runs whatever
// the cadence and retries a failed digest. A cycle that stops before
// importing records why in the state (Failure) and returns it.
func (r *Runner) Cycle(ctx context.Context, name string, force bool) (Result, error) {
	var res Result
	err := Lock(r.StateDir, name).Hold(name, func() error {
		var err error
		res, err = r.cycle(ctx, name, force)
		return err
	})
	return res, err
}

// fail records a failure and returns it.
func (r *Runner) fail(name string, st *State, digest string, err error) error {
	f := &Failure{At: r.now().UTC(), Digest: digest, Step: "build", Message: err.Error(), Log: LogPath(r.StateDir, name)}
	var se *StepError
	if errors.As(err, &se) {
		f.Repo, f.Commit, f.Step, f.Message = se.Repo, se.Commit, se.Step, se.Err.Error()
	}
	st.Failed = f
	if serr := SaveState(r.StateDir, name, st); serr != nil {
		return errors.Join(err, serr)
	}
	return err
}

func (r *Runner) cycle(ctx context.Context, name string, force bool) (Result, error) {
	now := r.now().UTC()
	st, err := LoadState(r.StateDir, name)
	if err != nil {
		return Result{}, err
	}
	if !force && !st.Due(now) {
		return Result{Skipped: "fetched " + now.Sub(st.Attempt).Round(time.Second).String() + " ago"}, nil
	}
	st.Attempt = now
	if err := SaveState(r.StateDir, name, st); err != nil {
		return Result{}, err
	}
	reg, err := Load(r.StateDir, name)
	if err != nil {
		return Result{}, r.fail(name, st, "", err)
	}
	env, err := gitEnv(r.StateDir)
	if err != nil {
		return Result{}, r.fail(name, st, "", err)
	}
	inputs, err := r.fetchAll(ctx, reg, st, env)
	if serr := SaveState(r.StateDir, name, st); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return Result{}, r.fail(name, st, "", err)
	}
	digest := r.digest(inputs)
	if have, ok, err := r.Store.Newest(ctx, name, "main"); err != nil {
		return Result{}, err
	} else if ok && have.InputsDigest == digest {
		st.Imported = digest
		st.Failed = nil
		return Result{Skipped: "unchanged"}, SaveState(r.StateDir, name, st)
	}
	if !force && st.held(digest, now) {
		return Result{Skipped: "these inputs failed " + now.Sub(st.Failed.At).Round(time.Minute).String() + " ago"}, nil
	}
	snap, err := r.buildImport(ctx, name, "main", inputs, digest, now)
	if err != nil {
		return Result{}, r.fail(name, st, digest, err)
	}
	st.Imported, st.ImportedAt, st.Failed = digest, now, nil
	return Result{Imported: true, Snapshot: snap}, SaveState(r.StateDir, name, st)
}

func (r *Runner) buildImport(ctx context.Context, name, source string, inputs []Input, digest string, now time.Time) (string, error) {
	timeout := r.BuildTimeout
	if timeout == 0 {
		timeout = rebuild.Timeout
	}
	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var scratch string
	var err error
	if source == "head" {
		// No lock: a one-off build beside any main cycle's scratch.
		if err = os.MkdirAll(filepath.Join(r.StateDir, "head"), 0o755); err == nil {
			scratch, err = os.MkdirTemp(filepath.Join(r.StateDir, "head"), "build-")
		}
	} else {
		scratch, err = Lock(r.StateDir, name).Scratch(name)
	}
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	work := filepath.Join(scratch, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	self := r.Self
	if self == "" {
		if self, err = os.Executable(); err != nil {
			return "", err
		}
	}
	out := filepath.Join(scratch, "snapshot")
	m, err := Build(bctx, BuildOptions{
		Name: name, Source: source, Inputs: inputs, Out: out, Work: work,
		PluginsDir: r.PluginsDir, PluginsDigest: r.PluginsDigest, CacheDir: r.CacheDir,
		Self: self, Mise: r.Mise, SchemaDB: r.SchemaDB, Digest: digest, Version: r.Version, Now: now, Log: r.Log,
	})
	if err != nil {
		if errors.Is(bctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("%w (the build stopped after its %s limit)", err, timeout)
		}
		return "", err
	}
	if _, err := r.Store.Import(ctx, out); err != nil {
		return "", &StepError{Step: "import", Err: err}
	}
	if err := r.Store.Prune(ctx, store.DefaultKeep); err != nil {
		return "", err
	}
	r.logf("imported %s", m.ID)
	return m.ID, nil
}

// digest is the inputs digest: each member's frozen commit (which fixes its
// settings and pins), the tools named, the plugins and the version.
func (r *Runner) digest(inputs []Input) string {
	h := sha256.New()
	fmt.Fprintf(h, "cartographer-inputs v1\nversion=%s\nplugins=%s\n", r.Version, r.PluginsDigest)
	sorted := append([]Input(nil), inputs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })
	for _, in := range sorted {
		fmt.Fprintf(h, "repo %s %s %s\n", in.Name(), strings.ToLower(in.Repo), in.Commit)
		for _, t := range toolsOf(in) {
			fmt.Fprintf(h, "  tool %s\n", t.Spec())
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// toolsOf lists the tools a member's extractors (and schema step) name;
// missing pins fail later, at the build.
func toolsOf(in Input) []Tool {
	var out []Tool
	steps := append([]string(nil), in.Settings.Extract...)
	if in.Settings.Schema != nil {
		steps = append(steps, "schema")
	}
	for _, s := range steps {
		ts, _ := ExtractorTools(s, in.Pins)
		out = append(out, ts...)
	}
	return out
}

// locate returns the member's recorded checkout if it is still there, else
// looks for it again (row 6).
func (r *Runner) locate(ctx context.Context, reg *Registration, slug string) string {
	if m, ok := reg.Member(slug); ok && m.Path != "" && isCheckout(m.Path) && Identify(ctx, m.Path) != "" && strings.EqualFold(Identify(ctx, m.Path), slug) {
		return m.Path
	}
	l := &Locator{Checkouts: r.Checkouts, Near: reg.Entry, Roots: r.Roots}
	return l.Find(ctx, slug)
}

// fetchAll fetches the anchor, reads the member list from its frozen
// commit, fetches the members in parallel within FetchBudget, and returns
// every member's input. Results go into st; the registration is updated
// with the list as read and the paths found.
func (r *Runner) fetchAll(ctx context.Context, reg *Registration, st *State, env []string) ([]Input, error) {
	fctx, cancel := context.WithTimeout(ctx, FetchBudget)
	defer cancel()
	var mu sync.Mutex
	fetchOne := func(m Member) (string, error) {
		branch := m.Branch
		var ferr error
		commit := ""
		if branch == "" {
			if branch, ferr = DefaultBranch(fctx, env, m.Path, m.URL()); ferr != nil {
				mu.Lock()
				branch = st.Fetch[m.Name()].Branch // the one fetched before, if any
				mu.Unlock()
			}
		}
		if ferr == nil {
			commit, ferr = Fetch(fctx, env, m.Path, m.Repo, branch)
		}
		res := FetchResult{At: r.now().UTC(), Branch: branch}
		mu.Lock()
		prev := st.Fetch[m.Name()]
		mu.Unlock()
		res.LastOK = prev.LastOK
		if ferr != nil {
			res.Error = ferr.Error()
			if branch != "" {
				commit, _ = Fetched(ctx, m.Path, branch)
			}
		} else {
			res.LastOK = res.At
		}
		res.Commit = commit
		mu.Lock()
		st.Fetch[m.Name()] = res
		mu.Unlock()
		if commit == "" {
			if ferr == nil {
				ferr = errors.New("no fetched commit")
			}
			return "", &StepError{m.Repo, "", "fetch", fmt.Errorf("never fetched successfully: %v", ferr)}
		}
		if ferr != nil {
			r.logf("%s: fetch failed, using the last fetched commit %s: %v", m.Name(), short(commit), ferr)
		} else {
			r.logf("%s: fetched %s @ %s", m.Name(), branch, short(commit))
		}
		return commit, nil
	}

	// The anchor first: its frozen commit holds the member list.
	anchor, _ := reg.Member(reg.Anchor)
	anchor.Repo = reg.Anchor
	if anchor.Path = r.locate(ctx, reg, reg.Anchor); anchor.Path == "" {
		return nil, &StepError{reg.Anchor, "", "checkout", fmt.Errorf("no checkout (nothing is cloned): %s", CloneCommand(reg.Anchor, r.Roots))}
	}
	ac, err := fetchOne(anchor)
	if err != nil {
		return nil, err
	}
	as, err := readSettings(ctx, anchor.Path, ac, reg.Anchor)
	if err != nil {
		return nil, err
	}
	if as.Project != reg.Name {
		return nil, &StepError{reg.Anchor, ac, "settings", fmt.Errorf("%s names project %q, but it is registered as %q (cartographer project remove %s, then project add again)", SettingsFile, as.Project, reg.Name, reg.Name)}
	}
	if as.Anchor != "" {
		return nil, &StepError{reg.Anchor, ac, "settings", fmt.Errorf("the anchor's %s names another anchor, %s", SettingsFile, as.Anchor)}
	}
	list := as.MemberList(reg.Anchor)
	members := make([]Member, len(list))
	var missing []string
	for i, spec := range list {
		members[i] = Member{Repo: spec.Repo, Branch: spec.Branch}
		if strings.EqualFold(spec.Repo, reg.Anchor) {
			members[i].Path = anchor.Path
		} else if members[i].Path = r.locate(ctx, reg, spec.Repo); members[i].Path == "" {
			missing = append(missing, spec.Repo)
		}
	}
	reg.Members = members
	if err := Save(r.StateDir, reg); err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		var cmds []string
		for _, m := range missing {
			cmds = append(cmds, CloneCommand(m, r.Roots))
		}
		return nil, &StepError{missing[0], "", "checkout", fmt.Errorf("no checkout for %s (nothing is cloned): %s", strings.Join(missing, ", "), strings.Join(cmds, "; "))}
	}

	inputs := make([]Input, len(members))
	errs := make([]error, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		if strings.EqualFold(m.Repo, reg.Anchor) {
			inputs[i] = Input{Member: m, Commit: ac}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := fetchOne(m)
			inputs[i], errs[i] = Input{Member: m, Commit: c}, err
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		for _, e := range errs {
			if e != nil {
				return nil, e
			}
		}
	}
	// Settings and pins, from each frozen commit.
	for i := range inputs {
		in := &inputs[i]
		s := as
		if !strings.EqualFold(in.Repo, reg.Anchor) {
			if s, err = readSettings(ctx, in.Path, in.Commit, in.Repo); err != nil {
				return nil, err
			}
		}
		if err := checkMember(s, in.Repo, in.Commit, reg.Name, reg.Anchor); err != nil {
			return nil, err
		}
		in.Settings = s
		pins, err := ResolvePins(func(p string) ([]byte, error) { return ReadAt(ctx, in.Path, in.Commit, p) }, s)
		if err != nil {
			return nil, &StepError{in.Repo, in.Commit, "tools", err}
		}
		in.Pins = pins
	}
	// The anchor goes first (its contexts_depth applies).
	sort.SliceStable(inputs, func(i, j int) bool {
		return strings.EqualFold(inputs[i].Repo, reg.Anchor) && !strings.EqualFold(inputs[j].Repo, reg.Anchor)
	})
	return inputs, nil
}

// ResolvePins is the one place a member's language versions come from:
// the repo's own pin files (Pins). s is the member's settings, for a
// policy that would also consult them.
func ResolvePins(read func(string) ([]byte, error), s *Settings) (map[string]string, error) {
	return Pins(read)
}

// checkMember fails closed on a member that doesn't line up with the
// project it is listed in.
func checkMember(s *Settings, slug, commit, project, anchor string) error {
	if s.Project != project {
		return &StepError{slug, commit, "settings", fmt.Errorf("%s names project %q, not %q", SettingsFile, s.Project, project)}
	}
	want := anchor
	if strings.EqualFold(slug, anchor) {
		want = ""
	}
	if !strings.EqualFold(s.Anchor, want) {
		got := s.Anchor
		if got == "" {
			got = "itself"
		}
		return &StepError{slug, commit, "settings", fmt.Errorf("%s names anchor %s, not %s", SettingsFile, got, anchor)}
	}
	return nil
}

// readSettings reads and parses a member's repo.toml at commit.
func readSettings(ctx context.Context, dir, commit, slug string) (*Settings, error) {
	b, err := ReadAt(ctx, dir, commit, SettingsFile)
	if errors.Is(err, ErrNoFile) {
		return nil, &StepError{slug, commit, "settings", fmt.Errorf("no %s on its fetched branch: run `cartographer project init %s` to write a starter one, then commit it to the branch", SettingsFile, dir)}
	}
	if err != nil {
		return nil, &StepError{slug, commit, "settings", err}
	}
	s, err := ParseSettings(b, slug)
	if err != nil {
		return nil, &StepError{slug, commit, "settings", fmt.Errorf("%s: %w", SettingsFile, err)}
	}
	return s, nil
}

// readWorkingSettings reads a member's repo.toml from its working copy.
func readWorkingSettings(dir, slug string) (*Settings, error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(SettingsFile)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, &StepError{slug, "", "settings", fmt.Errorf("no %s in %s: run `cartographer project init %s` to write a starter one", SettingsFile, dir, dir)}
	}
	if err != nil {
		return nil, &StepError{slug, "", "settings", err}
	}
	s, err := ParseSettings(b, slug)
	if err != nil {
		return nil, &StepError{slug, "", "settings", fmt.Errorf("%s: %w", SettingsFile, err)}
	}
	return s, nil
}

// Head builds the working copies of the project that from (a member
// checkout) belongs to, as they are, settings included, and imports the
// result with source "head" ("your checkouts"). It needs no registration
// and keeps no state.
func (r *Runner) Head(ctx context.Context, from string) (Result, error) {
	root := fresh.Root(ctx, from)
	if root == "" {
		return Result{}, fmt.Errorf("%s is not in a git checkout", from)
	}
	slug := Identify(ctx, root)
	if slug == "" {
		return Result{}, fmt.Errorf("%s has no GitHub remote", root)
	}
	s, err := readWorkingSettings(root, slug)
	if err != nil {
		return Result{}, err
	}
	anchor, aroot, as := slug, root, s
	l := &Locator{Checkouts: r.Checkouts, Near: root, Roots: r.Roots}
	if s.Anchor != "" {
		anchor = s.Anchor
		if aroot = l.Find(ctx, anchor); aroot == "" {
			return Result{}, &StepError{anchor, "", "checkout", fmt.Errorf("no checkout: %s", CloneCommand(anchor, r.Roots))}
		}
		if as, err = readWorkingSettings(aroot, anchor); err != nil {
			return Result{}, err
		}
	}
	var inputs []Input
	for _, spec := range as.MemberList(anchor) {
		m := Member{Repo: spec.Repo, Branch: spec.Branch}
		switch {
		case strings.EqualFold(spec.Repo, anchor):
			m.Path = aroot
		case strings.EqualFold(spec.Repo, slug):
			m.Path = root
		default:
			if m.Path = l.Find(ctx, spec.Repo); m.Path == "" {
				return Result{}, &StepError{spec.Repo, "", "checkout", fmt.Errorf("no checkout: %s", CloneCommand(spec.Repo, r.Roots))}
			}
		}
		ms := as
		if !strings.EqualFold(spec.Repo, anchor) {
			if ms, err = readWorkingSettings(m.Path, spec.Repo); err != nil {
				return Result{}, err
			}
		}
		if err := checkMember(ms, spec.Repo, "", as.Project, anchor); err != nil {
			return Result{}, err
		}
		pins, err := ResolvePins(func(p string) ([]byte, error) {
			b, err := os.ReadFile(filepath.Join(m.Path, filepath.FromSlash(p)))
			if errors.Is(err, os.ErrNotExist) {
				return nil, ErrNoFile
			}
			return b, err
		}, ms)
		if err != nil {
			return Result{}, &StepError{spec.Repo, "", "tools", err}
		}
		inputs = append(inputs, Input{Member: m, Commit: fresh.Head(ctx, m.Path), Head: true, Settings: ms, Pins: pins})
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		return strings.EqualFold(inputs[i].Repo, anchor) && !strings.EqualFold(inputs[j].Repo, anchor)
	})
	snap, err := r.buildImport(ctx, as.Project, "head", inputs, "", r.now().UTC())
	if err != nil {
		return Result{}, err
	}
	return Result{Imported: true, Snapshot: snap}, nil
}
