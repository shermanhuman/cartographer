package render

import (
	"context"
	"fmt"
	"strings"

	"github.com/shermanhuman/cartographer/internal/query"
)

// maxSourceLinks bounds the pinned source list in an embed.
const maxSourceLinks = 25

// Embed renders GitHub issue Markdown for one entity: a title, a summary
// table, a Mermaid neighbourhood and source links pinned to the snapshot's
// repository revisions, ending with the map provenance footer.
func Embed(ctx context.Context, q *query.Q, ref string) (string, error) {
	nb, err := buildNeighbourhood(ctx, q, ref, 1, DefaultMaxNodes)
	if err != nil {
		return "", err
	}
	repoList, err := q.Repos(ctx)
	if err != nil {
		return "", err
	}
	repos := map[string]query.Repo{}
	for _, r := range repoList {
		repos[r.Name] = r
	}
	e := nb.Entity
	link := func(label, repo, file string, line int) string {
		loc := file
		if line > 0 {
			loc = fmt.Sprintf("%s:%d", file, line)
		}
		if u := query.BlobURL(repos[repo], file, line); u != "" {
			return fmt.Sprintf("[`%s`](%s)", label, u)
		}
		if loc != "" {
			return fmt.Sprintf("`%s` (%s)", label, loc)
		}
		return "`" + label + "`"
	}

	startSet := map[string]bool{}
	repoOf := map[string]string{}
	for _, n := range nb.Nodes {
		repoOf[n.ID] = n.Repo
	}
	for _, id := range nb.Start {
		startSet[id] = true
	}
	cross := 0
	for _, h := range nb.Callers {
		if h.Repo != repoOf[h.Via] && repoOf[h.Via] != "" {
			cross++
		}
	}
	var unresolved int
	for _, id := range nb.Start {
		var n int
		q.S.DB.QueryRowContext(ctx, `SELECT count(*) FROM calls WHERE snapshot_id = ? AND from_id = ? AND to_id IS NULL`, q.Snapshot, id).Scan(&n)
		unresolved += n
	}

	var b strings.Builder
	fmt.Fprintf(&b, "### `%s` · cartographer\n\n", e.Label)
	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Kind | %s |\n", e.Kind)
	switch {
	case e.File != "":
		loc := e.File
		if e.Line > 0 {
			loc = fmt.Sprintf("%s:%d", e.File, e.Line)
		}
		if u := query.BlobURL(repos[e.Repo], e.File, e.Line); u != "" {
			loc = fmt.Sprintf("[`%s`](%s)", loc, u)
		} else {
			loc = "`" + loc + "`"
		}
		fmt.Fprintf(&b, "| Location | %s · %s |\n", loc, e.Repo)
	case e.Repo != "":
		fmt.Fprintf(&b, "| Location | %s |\n", e.Repo)
	}
	if e.Kind != "function" {
		fmt.Fprintf(&b, "| Functions | %d |\n", len(nb.Start))
	}
	fmt.Fprintf(&b, "| Callers | %d |\n", len(nb.Callers))
	fmt.Fprintf(&b, "| Callees | %d |\n", len(nb.Callees))
	fmt.Fprintf(&b, "| Cross-repo callers | %d |\n", cross)
	if unresolved > 0 {
		fmt.Fprintf(&b, "| Unresolved call sites | %d (dynamic, imported or outside the map) |\n", unresolved)
	}
	b.WriteString("\n```mermaid\n")
	b.WriteString(nb.mermaid(""))
	b.WriteString("```\n\n")

	type src struct {
		role string
		ent  query.Entity
	}
	var srcs []src
	for _, id := range nb.Start {
		if ent, _ := q.Get(ctx, id); ent != nil {
			srcs = append(srcs, src{"", *ent})
		}
	}
	for _, h := range nb.Callers {
		srcs = append(srcs, src{"caller", h.Entity})
	}
	for _, h := range nb.Callees {
		srcs = append(srcs, src{"callee", h.Entity})
	}
	if len(srcs) > 0 {
		b.WriteString("**Source** (pinned to the snapshot revisions)\n\n")
		for i, s := range srcs {
			if i == maxSourceLinks {
				fmt.Fprintf(&b, "- …+%d more (`cartographer show %s`)\n", len(srcs)-i, e.ID)
				break
			}
			role := ""
			if s.role != "" {
				role = s.role + " "
			}
			cr := ""
			if s.role == "caller" && e.Repo != "" && s.ent.Repo != e.Repo {
				cr = " ⇄ cross-repo"
			}
			fmt.Fprintf(&b, "- %s%s%s\n", role, link(s.ent.Label, s.ent.Repo, s.ent.File, s.ent.Line), cr)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "<sub>%s · <code>cartographer show %s</code></sub>\n", q.Provenance(ctx), e.ID)
	return capChars(b.String()), nil
}

// capChars truncates Markdown at MaxChars on a line boundary.
func capChars(s string) string {
	if len(s) <= MaxChars {
		return s
	}
	cut := strings.LastIndex(s[:MaxChars-120], "\n")
	if cut < 0 {
		cut = MaxChars - 120
	}
	s = s[:cut]
	if strings.Count("\n"+s, "\n```")%2 == 1 {
		s += "\n```" // close an open fence
	}
	return s + "\n\n_…truncated to stay under GitHub's comment limit; run the command locally for the full result._\n"
}
