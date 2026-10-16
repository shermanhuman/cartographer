// Package render turns map queries into shareable Markdown: Mermaid call
// graphs, GitHub issue embeds and snapshot-to-snapshot PR impact comments.
// It never imports MCP or HTTP code; callers wire it into those surfaces.
package render

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/query"
)

// DefaultMaxNodes caps Mermaid diagrams; GitHub renders larger graphs poorly.
const DefaultMaxNodes = 40

// Bounds for caller-supplied diagram options (CLI, HTTP and MCP).
const (
	MaxNodesLimit = 200
	MaxDepth      = 5
)

// ClampDiagram bounds depth to 1..MaxDepth (default 1) and max to
// 1..MaxNodesLimit (default DefaultMaxNodes).
func ClampDiagram(depth, max int) (int, int) {
	if depth <= 0 {
		depth = 1
	}
	if max <= 0 {
		max = DefaultMaxNodes
	}
	return min(depth, MaxDepth), min(max, MaxNodesLimit)
}

// MaxChars keeps generated Markdown under GitHub's 65,536-character
// comment limit with room for a wrapper.
const MaxChars = 60000

type mnode struct {
	ID    string // entity id
	Label string
	Repo  string
	Class string // focus, added, removed, changed, or ""
}

type mlink struct {
	From, To string
	Label    string
	Dotted   bool
	Style    string // linkStyle body, e.g. "stroke:#d73a49"
}

// classDefs are shared by every diagram so colours mean the same thing.
var classDefs = []string{
	"classDef focus fill:#fff3bf,stroke:#f08c00,stroke-width:2px",
	"classDef added fill:#d3f9d8,stroke:#2b8a3e",
	"classDef removed fill:#ffe3e3,stroke:#c92a2a,stroke-dasharray:4 3",
	"classDef changed fill:#fff3bf,stroke:#e67700",
	"classDef note fill:#f1f3f5,stroke:#adb5bd,color:#495057",
}

// mermaidText escapes text for a quoted Mermaid label.
func mermaidText(s string) string {
	r := strings.NewReplacer(`"`, "#quot;", "<", "#lt;", ">", "#gt;", "\n", " ", "`", "#96;", "|", "#124;")
	return r.Replace(s)
}

// writeMermaid renders nodes and links as `graph LR`. Node ids are replaced
// by n0, n1, … so arbitrary entity ids never break the syntax; nodes are
// grouped into one subgraph per repository when several repositories appear.
func writeMermaid(b *strings.Builder, comment string, nodes []mnode, links []mlink, more string) {
	b.WriteString("graph LR\n")
	if comment != "" {
		fmt.Fprintf(b, "  %%%% %s\n", strings.ReplaceAll(comment, "\n", " "))
	}
	ids := map[string]string{}
	for i, n := range nodes {
		ids[n.ID] = fmt.Sprintf("n%d", i)
	}
	var repos []string
	byRepo := map[string][]int{}
	for i, n := range nodes {
		if _, ok := byRepo[n.Repo]; !ok {
			repos = append(repos, n.Repo)
		}
		byRepo[n.Repo] = append(byRepo[n.Repo], i)
	}
	nodeLine := func(indent string, i int) {
		fmt.Fprintf(b, "%s%s[\"%s\"]\n", indent, ids[nodes[i].ID], mermaidText(nodes[i].Label))
	}
	if len(repos) > 1 {
		for ri, repo := range repos {
			name := repo
			if name == "" {
				name = "other"
			}
			fmt.Fprintf(b, "  subgraph r%d[\"%s\"]\n", ri, mermaidText(name))
			for _, i := range byRepo[repo] {
				nodeLine("    ", i)
			}
			b.WriteString("  end\n")
		}
	} else {
		for i := range nodes {
			nodeLine("  ", i)
		}
	}
	var styles []string
	n := 0
	for _, l := range links {
		from, ok1 := ids[l.From]
		to, ok2 := ids[l.To]
		if !ok1 || !ok2 {
			continue
		}
		arrow := "-->"
		if l.Dotted {
			arrow = "-.->"
		}
		if l.Label != "" {
			fmt.Fprintf(b, "  %s %s|\"%s\"| %s\n", from, arrow, mermaidText(l.Label), to)
		} else {
			fmt.Fprintf(b, "  %s %s %s\n", from, arrow, to)
		}
		if l.Style != "" {
			styles = append(styles, fmt.Sprintf("  linkStyle %d %s", n, l.Style))
		}
		n++
	}
	if more != "" {
		fmt.Fprintf(b, "  more[\"%s\"]\n", mermaidText(more))
	}
	for _, s := range styles {
		b.WriteString(s + "\n")
	}
	for _, c := range classDefs {
		b.WriteString("  " + c + "\n")
	}
	classes := map[string][]string{}
	var order []string
	for _, nd := range nodes {
		if nd.Class == "" {
			continue
		}
		if _, ok := classes[nd.Class]; !ok {
			order = append(order, nd.Class)
		}
		classes[nd.Class] = append(classes[nd.Class], ids[nd.ID])
	}
	for _, c := range order {
		fmt.Fprintf(b, "  class %s %s\n", strings.Join(classes[c], ","), c)
	}
	if more != "" {
		b.WriteString("  class more note\n")
	}
}

func edgeLabel(sites int, precision string) string {
	var parts []string
	if precision != "" && precision != "precise" {
		parts = append(parts, precision)
	}
	if sites > 1 {
		parts = append(parts, fmt.Sprintf("×%d", sites))
	}
	return strings.Join(parts, " ")
}

// neighbourhood is an entity, the functions it stands for and its callers
// and callees to depth, ordered nearest first and capped at max nodes.
type neighbourhood struct {
	Entity  *query.Entity
	Start   []string
	Callers []query.Hop
	Callees []query.Hop
	Nodes   []mnode
	Links   []mlink
	Hidden  int
}

func buildNeighbourhood(ctx context.Context, q *query.Q, ref string, depth, max int) (*neighbourhood, error) {
	e, err := q.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	depth, max = ClampDiagram(depth, max)
	start, _, err := q.FunctionsOf(ctx, e)
	if err != nil {
		return nil, err
	}
	nb := &neighbourhood{Entity: e, Start: start}
	if nb.Callers, err = q.Walk(ctx, start, depth, true); err != nil {
		return nil, err
	}
	if nb.Callees, err = q.Walk(ctx, start, depth, false); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var all []mnode
	add := func(id, label, repo, class string) {
		if seen[id] {
			return
		}
		seen[id] = true
		all = append(all, mnode{ID: id, Label: label, Repo: repo, Class: class})
	}
	for _, id := range start {
		ent, err := q.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if ent != nil {
			add(id, ent.Label, ent.Repo, "focus")
		}
	}
	for d := 1; d <= depth; d++ {
		for _, hs := range [][]query.Hop{nb.Callers, nb.Callees} {
			level := []query.Hop{}
			for _, h := range hs {
				if h.Depth == d {
					level = append(level, h)
				}
			}
			sort.Slice(level, func(i, j int) bool { return level[i].ID < level[j].ID })
			for _, h := range level {
				add(h.ID, h.Label, h.Repo, "")
			}
		}
	}
	if len(all) > max {
		nb.Hidden = len(all) - max
		all = all[:max]
	}
	nb.Nodes = all
	ids := make([]string, len(all))
	keep := map[string]bool{}
	for i, n := range all {
		ids[i] = n.ID
		keep[n.ID] = true
	}
	edges, err := q.Edges(ctx, ids, false)
	if err != nil {
		return nil, err
	}
	for _, ed := range edges {
		if !keep[ed.To] || ed.From == ed.To {
			continue
		}
		nb.Links = append(nb.Links, mlink{From: ed.From, To: ed.To, Label: edgeLabel(ed.Sites, ed.Precision), Dotted: ed.Precision != "" && ed.Precision != "precise"})
	}
	return nb, nil
}

// Mermaid renders the entity and its callers and callees to depth (default
// 1, at most MaxDepth) as a `graph LR` diagram of at most max nodes (default
// 40, at most MaxNodesLimit) and MaxChars characters. Hidden nodes are
// counted in a note pointing at `cartographer impact`.
func Mermaid(ctx context.Context, q *query.Q, ref string, depth, max int) (string, error) {
	nb, err := buildNeighbourhood(ctx, q, ref, depth, max)
	if err != nil {
		return "", err
	}
	return nb.mermaid(q.Provenance(ctx)), nil
}

// mermaid renders the diagram, dropping the farthest nodes until it fits in
// MaxChars (so it stays a valid diagram), with capChars as a last resort.
func (nb *neighbourhood) mermaid(comment string) string {
	nodes, hidden := nb.Nodes, nb.Hidden
	for {
		var b strings.Builder
		more := ""
		if hidden > 0 {
			more = fmt.Sprintf("+%d more → cartographer impact %s", hidden, nb.Entity.ID)
		}
		writeMermaid(&b, comment, nodes, nb.Links, more)
		out := b.String()
		if len(out) <= MaxChars || len(nodes) <= 1 {
			return capChars(out)
		}
		keep := max(1, min(len(nodes)-1, len(nodes)*MaxChars*9/(len(out)*10)))
		hidden += len(nodes) - keep
		nodes = nodes[:keep]
	}
}
