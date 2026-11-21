package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shermanhuman/cartographer/internal/fresh"
)

// AddPlan is what `project add` would register.
type AddPlan struct {
	Reg *Registration
	// Missing are members without a checkout, with their clone commands.
	Missing []string
}

// Plan prepares a registration from arg, a member checkout or owner/name:
// it fetches that repo, reads its repo.toml from the fetched commit,
// follows `anchor` (fetched and read first), requires the repo to be in
// the anchor's list and locates every member. Nothing is registered.
func (r *Runner) Plan(ctx context.Context, arg string) (*AddPlan, error) {
	var entry, slug string
	if fi, err := os.Stat(arg); err == nil && fi.IsDir() {
		if entry = fresh.Root(ctx, arg); entry == "" {
			return nil, fmt.Errorf("%s is not in a git checkout", arg)
		}
		if slug = Identify(ctx, entry); slug == "" {
			return nil, fmt.Errorf("%s has no remote on github.com: a project member is fetched from https://github.com/<owner>/<name>", entry)
		}
	} else if ValidSlug(arg) {
		slug = arg
		l := &Locator{Checkouts: r.Checkouts, Roots: r.Roots}
		if entry = l.Find(ctx, slug); entry == "" {
			return nil, fmt.Errorf("no checkout of %s (nothing is cloned): %s, then run project add again", slug, CloneCommand(slug, r.Roots))
		}
	} else {
		return nil, fmt.Errorf("project add: %q is neither a checkout directory nor owner/name", arg)
	}
	env, err := gitEnv(r.StateDir)
	if err != nil {
		return nil, err
	}
	fctx, cancel := context.WithTimeout(ctx, FetchBudget)
	defer cancel()
	fetch := func(dir, slug, branch string) (string, error) {
		if branch == "" {
			if branch, err = DefaultBranch(fctx, env, dir, GitHubURL(slug)); err != nil {
				return "", &StepError{slug, "", "fetch", err}
			}
		}
		c, err := Fetch(fctx, env, dir, slug, branch)
		if err != nil {
			return "", &StepError{slug, "", "fetch", err}
		}
		return c, nil
	}
	ec, err := fetch(entry, slug, "")
	if err != nil {
		return nil, err
	}
	es, err := readSettings(ctx, entry, ec, slug)
	if err != nil {
		return nil, err
	}
	anchor, adir, as := slug, entry, es
	l := &Locator{Checkouts: r.Checkouts, Near: entry, Roots: r.Roots}
	if es.Anchor != "" {
		anchor = es.Anchor
		if adir = l.Find(ctx, anchor); adir == "" {
			return nil, fmt.Errorf("%s's anchor %s has no checkout (nothing is cloned): %s", slug, anchor, CloneCommand(anchor, r.Roots))
		}
		ac, err := fetch(adir, anchor, "")
		if err != nil {
			return nil, err
		}
		if as, err = readSettings(ctx, adir, ac, anchor); err != nil {
			return nil, err
		}
		if as.Anchor != "" {
			return nil, &StepError{anchor, ac, "settings", fmt.Errorf("%s names anchor %s, but %s names it as the anchor", SettingsFile, as.Anchor, slug)}
		}
		if as.Project != es.Project {
			return nil, &StepError{slug, ec, "settings", fmt.Errorf("%s names project %q, its anchor %s names %q", SettingsFile, es.Project, anchor, as.Project)}
		}
	}
	list := as.MemberList(anchor)
	inList := false
	for _, m := range list {
		inList = inList || strings.EqualFold(m.Repo, slug)
	}
	if !inList {
		return nil, fmt.Errorf("%s isn't in %s's member list (members in its %s)", slug, anchor, SettingsFile)
	}
	plan := &AddPlan{Reg: &Registration{Name: as.Project, Anchor: anchor, Entry: entry, Store: r.DB}}
	for _, spec := range list {
		m := Member{Repo: spec.Repo, Branch: spec.Branch}
		switch {
		case strings.EqualFold(spec.Repo, anchor):
			m.Repo, m.Path = anchor, adir
		case strings.EqualFold(spec.Repo, slug):
			m.Path = entry
		default:
			if m.Path = l.Find(ctx, spec.Repo); m.Path == "" {
				plan.Missing = append(plan.Missing, CloneCommand(spec.Repo, r.Roots))
			}
		}
		plan.Reg.Members = append(plan.Reg.Members, m)
	}
	return plan, nil
}

// Init writes a starter .cartographer/repo.toml into the checkout at dir
// (project, anchor, the extractors its files suggest, docs = []) and
// returns the file and the git commands that commit it on a new branch. It
// commits nothing and never overwrites a repo.toml.
func Init(ctx context.Context, dir, project, anchor string) (file string, commands []string, err error) {
	root := fresh.Root(ctx, dir)
	if root == "" {
		return "", nil, fmt.Errorf("%s is not in a git checkout", dir)
	}
	slug := Identify(ctx, root)
	if slug == "" {
		return "", nil, fmt.Errorf("%s has no remote on github.com: a project member is fetched from https://github.com/<owner>/<name>", root)
	}
	file = filepath.Join(root, filepath.FromSlash(SettingsFile))
	if _, err := os.Stat(file); err == nil {
		return "", nil, fmt.Errorf("%s already exists; edit it instead", file)
	}
	if project == "" {
		return "", nil, errors.New("project init: --project is required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# cartographer settings for this repo, read from its default branch.\nproject = %q\n", project)
	if anchor != "" && !strings.EqualFold(anchor, slug) {
		fmt.Fprintf(&b, "anchor = %q\n", anchor)
	} else {
		fmt.Fprintf(&b, "# This repo is the anchor: it lists the members.\nmembers = [\n  { repo = %q },\n]\n", slug)
	}
	ex := detect(root)
	q := make([]string, len(ex))
	for i, e := range ex {
		q[i] = fmt.Sprintf("%q", e)
	}
	fmt.Fprintf(&b, "extract = [%s]\n# Markdown to include: [] is every tracked *.md, false none.\ndocs = []\n", strings.Join(q, ", "))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", nil, err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", nil, err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		return "", nil, err
	}
	return file, []string{
		fmt.Sprintf("git -C %s switch -c cartographer-settings", root),
		fmt.Sprintf("git -C %s add %s", root, SettingsDir),
		fmt.Sprintf("git -C %s commit -m %q", root, "Add cartographer settings (.cartographer/repo.toml)"),
	}, nil
}

// detect suggests extractors from the files at the top of a checkout.
func detect(root string) []string {
	has := func(p string) bool { _, err := os.Stat(filepath.Join(root, p)); return err == nil }
	var out []string
	if has("go.mod") {
		out = append(out, "go")
	}
	if has("Cargo.toml") {
		out = append(out, "rust")
	}
	if has("mix.exs") {
		out = append(out, "elixir")
	}
	if has("package.json") {
		out = append(out, "javascript")
	}
	for _, d := range []string{"k8s", "kubernetes", "manifests", "deploy", "charts"} {
		if has(d) {
			out = append(out, "kubernetes")
			break
		}
	}
	return out
}
