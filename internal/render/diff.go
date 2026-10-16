package render

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/store"
)

// maxListItems bounds every list in a diff comment.
const maxListItems = 50

// DiffDepth is how far the blast radius follows callers in the head snapshot.
const DiffDepth = 2

type dfn struct {
	ID, Repo, Module, Name, Label, File, FP string
	Arity, MinArity, Line                   int
}

type pair struct{ From, To string }

type refKey struct{ Repo, Table, Function, File string }

type snapFacts struct {
	id    string
	fns   map[string]dfn
	edges map[pair]string // best precision
	refs  map[refKey]int  // first line
	repos []query.Repo
}

func loadFacts(ctx context.Context, db *sql.DB, sid string) (*snapFacts, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM snapshots WHERE id = ?`, sid).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("diff: no snapshot %q in the store (see `cartographer snapshots`)", sid)
	}
	f := &snapFacts{id: sid, fns: map[string]dfn{}, edges: map[pair]string{}, refs: map[refKey]int{}}
	rows, err := db.QueryContext(ctx, `
	  SELECT f.id, f.repo, f.module, coalesce(m.name, ''), f.name, f.arity, f.min_arity, f.fingerprint, f.file, f.line
	    FROM functions f LEFT JOIN modules m ON m.snapshot_id = f.snapshot_id AND m.id = f.module
	   WHERE f.snapshot_id = ?`, sid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d dfn
		var mod string
		if err := rows.Scan(&d.ID, &d.Repo, &d.Module, &mod, &d.Name, &d.Arity, &d.MinArity, &d.FP, &d.File, &d.Line); err != nil {
			rows.Close()
			return nil, err
		}
		d.Label = fmt.Sprintf("%s/%d", d.Name, d.Arity)
		if mod != "" {
			d.Label = mod + "." + d.Label
		}
		f.fns[d.ID] = d
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `SELECT from_id, to_id, group_concat(DISTINCT precision) FROM calls WHERE snapshot_id = ? AND to_id IS NOT NULL GROUP BY 1, 2`, sid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p pair
		var precs string
		if err := rows.Scan(&p.From, &p.To, &precs); err != nil {
			rows.Close()
			return nil, err
		}
		best := ""
		for _, pr := range strings.Split(precs, ",") {
			if query.PrecisionRank(pr) > query.PrecisionRank(best) {
				best = pr
			}
		}
		f.edges[p] = best
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `SELECT repo, table_id, function_id, file, min(line) FROM table_refs WHERE snapshot_id = ? GROUP BY 1, 2, 3, 4`, sid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k refKey
		var line int
		if err := rows.Scan(&k.Repo, &k.Table, &k.Function, &k.File, &line); err != nil {
			rows.Close()
			return nil, err
		}
		f.refs[k] = line
	}
	rows.Close()
	q := &query.Q{S: &store.Store{DB: db}, Snapshot: sid}
	if f.repos, err = q.Repos(ctx); err != nil {
		return nil, err
	}
	return f, rows.Err()
}

type sigChange struct {
	Key        string // repo|module|name
	Label      string // Module.name
	Before     []int
	After      []int
	BaseIDs    []string
	HeadIDs    []string
	Repo, File string
	Line       int
}

func arities(fns []dfn) []int {
	set := map[int]bool{}
	for _, f := range fns {
		for a := f.MinArity; a <= f.Arity; a++ {
			set[a] = true
		}
	}
	out := make([]int, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Ints(out)
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joinArities(as []int) string {
	parts := make([]string, len(as))
	for i, a := range as {
		parts[i] = fmt.Sprintf("/%d", a)
	}
	return strings.Join(parts, ", ")
}

// Diff renders a PR comment comparing two imported snapshots: functions
// added, removed, signature-changed (same module and name, different arity
// set) and body-changed (fingerprint differs); the blast radius as callers
// in head to depth 2; newly referenced tables; and a Mermaid diagram of the
// changed nodes plus one hop (calls only in base are dotted red).
func Diff(ctx context.Context, s *store.Store, baseID, headID string) (string, error) {
	base, err := loadFacts(ctx, s.DB, baseID)
	if err != nil {
		return "", err
	}
	head, err := loadFacts(ctx, s.DB, headID)
	if err != nil {
		return "", err
	}

	group := func(fns map[string]dfn) map[string][]dfn {
		g := map[string][]dfn{}
		for _, f := range fns {
			k := f.Repo + "|" + f.Module + "|" + f.Name
			g[k] = append(g[k], f)
		}
		return g
	}
	bg, hg := group(base.fns), group(head.fns)
	var sigs []sigChange
	inSig := map[string]bool{}
	for k, bfs := range bg {
		hfs, ok := hg[k]
		if !ok {
			continue
		}
		ba, ha := arities(bfs), arities(hfs)
		if sameInts(ba, ha) {
			continue
		}
		sort.Slice(hfs, func(i, j int) bool { return hfs[i].Arity < hfs[j].Arity })
		label := strings.TrimSuffix(hfs[0].Label, fmt.Sprintf("/%d", hfs[0].Arity))
		sc := sigChange{Key: k, Label: label, Before: ba, After: ha, Repo: hfs[0].Repo, File: hfs[0].File, Line: hfs[0].Line}
		for _, f := range bfs {
			sc.BaseIDs = append(sc.BaseIDs, f.ID)
			inSig[f.ID] = true
		}
		for _, f := range hfs {
			sc.HeadIDs = append(sc.HeadIDs, f.ID)
			inSig[f.ID] = true
		}
		sort.Strings(sc.BaseIDs)
		sigs = append(sigs, sc)
	}
	sort.Slice(sigs, func(i, j int) bool { return sigs[i].Key < sigs[j].Key })

	var added, removed, body []dfn
	for id, f := range head.fns {
		if _, ok := base.fns[id]; !ok && !inSig[id] {
			added = append(added, f)
		}
	}
	for id, f := range base.fns {
		h, ok := head.fns[id]
		switch {
		case !ok && !inSig[id]:
			removed = append(removed, f)
		case ok && !inSig[id] && f.FP != "" && h.FP != "" && f.FP != h.FP:
			// A function already listed under a signature change is not
			// listed (or counted) again as a body change.
			body = append(body, h)
		}
	}
	byID := func(fs []dfn) { sort.Slice(fs, func(i, j int) bool { return fs[i].ID < fs[j].ID }) }
	byID(added)
	byID(removed)
	byID(body)

	var addedEdges, removedEdges []pair
	for p := range head.edges {
		if _, ok := base.edges[p]; !ok {
			addedEdges = append(addedEdges, p)
		}
	}
	for p := range base.edges {
		if _, ok := head.edges[p]; !ok {
			removedEdges = append(removedEdges, p)
		}
	}
	byPair := func(ps []pair) {
		sort.Slice(ps, func(i, j int) bool {
			if ps[i].From != ps[j].From {
				return ps[i].From < ps[j].From
			}
			return ps[i].To < ps[j].To
		})
	}
	byPair(addedEdges)
	byPair(removedEdges)

	type newRef struct {
		refKey
		Line int
	}
	var newRefs []newRef
	for k, line := range head.refs {
		if _, ok := base.refs[k]; !ok {
			newRefs = append(newRefs, newRef{k, line})
		}
	}
	sort.Slice(newRefs, func(i, j int) bool {
		a, b := newRefs[i], newRefs[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Function < b.Function
	})

	// Blast radius: callers in head of changed functions, plus head call
	// sites that no longer resolve (see danglingCalls).
	hq := &query.Q{S: s, Snapshot: headID}
	var seeds []string
	seedSet := map[string]bool{}
	addSeed := func(id string) {
		if !seedSet[id] {
			seedSet[id] = true
			seeds = append(seeds, id)
		}
	}
	for _, sc := range sigs {
		for _, id := range sc.HeadIDs {
			addSeed(id)
		}
	}
	for _, f := range body {
		addSeed(f.ID)
	}
	gone := append([]dfn{}, removed...)
	for _, sc := range sigs {
		for _, id := range sc.BaseIDs {
			if _, ok := head.fns[id]; !ok {
				gone = append(gone, base.fns[id])
			}
		}
	}
	dangles, err := danglingCalls(ctx, s.DB, baseID, headID, head, gone)
	if err != nil {
		return "", err
	}
	rootRepo := map[string]string{}
	for _, id := range seeds {
		rootRepo[id] = head.fns[id].Repo
	}
	hops, err := hq.Walk(ctx, seeds, DiffDepth, true)
	if err != nil {
		return "", err
	}
	var danglingHops []query.Hop
	if len(dangles) > 0 {
		var from []string
		seen := map[string]bool{}
		for _, d := range dangles {
			if !seen[d.From] && !seedSet[d.From] {
				seen[d.From] = true
				from = append(from, d.From)
				rootRepo[d.From] = head.fns[d.From].Repo
			}
		}
		if danglingHops, err = hq.Walk(ctx, from, DiffDepth-1, true); err != nil {
			return "", err
		}
	}
	for _, h := range hops {
		rootRepo[h.ID] = rootRepo[h.Via]
	}
	for _, h := range danglingHops {
		rootRepo[h.ID] = rootRepo[h.Via]
	}

	repos := map[string]query.Repo{}
	for _, r := range head.repos {
		repos[r.Name] = r
	}
	link := func(label, repo, file string, line int) string {
		if u := query.BlobURL(repos[repo], file, line); u != "" {
			return fmt.Sprintf("[`%s`](%s)", label, u)
		}
		return "`" + label + "`"
	}
	loc := func(file string, line int) string {
		if line > 0 {
			return fmt.Sprintf("%s:%d", file, line)
		}
		return file
	}

	// Count affected callers (unique, not counting seeds).
	affected := map[string]bool{}
	crossN := 0
	for _, list := range [][]query.Hop{hops, danglingHops} {
		for _, h := range list {
			if seedSet[h.ID] || affected[h.ID] {
				continue
			}
			affected[h.ID] = true
			if h.Repo != rootRepo[h.ID] {
				crossN++
			}
		}
	}
	for _, d := range dangles {
		if !affected[d.From] {
			affected[d.From] = true
		}
	}
	changedN := len(sigs) + len(body)

	var b strings.Builder
	b.WriteString("## cartographer impact\n\n")
	if changedN+len(added)+len(removed)+len(addedEdges)+len(removedEdges)+len(newRefs) == 0 {
		b.WriteString("No function, call or table-reference changes between these snapshots.\n\n")
	} else {
		fmt.Fprintf(&b, "**%d** changed · **%d** added · **%d** removed functions · calls **+%d / −%d** · **%d** callers affected (%d cross-repo) · **%d** new table reference(s)\n\n",
			changedN, len(added), len(removed), len(addedEdges), len(removedEdges), len(affected), crossN, len(newRefs))
	}

	type row struct{ change, fn, where string }
	var rows []row
	for _, sc := range sigs {
		rows = append(rows, row{"signature", link(sc.Label, sc.Repo, sc.File, sc.Line) + " " + joinArities(sc.Before) + " → " + joinArities(sc.After), sc.Repo + " " + loc(sc.File, sc.Line)})
	}
	for _, f := range body {
		rows = append(rows, row{"body", link(f.Label, f.Repo, f.File, f.Line), f.Repo + " " + loc(f.File, f.Line)})
	}
	for _, f := range added {
		rows = append(rows, row{"added", link(f.Label, f.Repo, f.File, f.Line), f.Repo + " " + loc(f.File, f.Line)})
	}
	for _, f := range removed {
		rows = append(rows, row{"removed", "`" + f.Label + "`", f.Repo + " " + loc(f.File, f.Line) + " (base)"})
	}
	if len(rows) > 0 {
		b.WriteString("### Changed functions\n\n| Change | Function | Where |\n|---|---|---|\n")
		for i, r := range rows {
			if i == maxListItems {
				fmt.Fprintf(&b, "| … | +%d more | |\n", len(rows)-i)
				break
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", r.change, r.fn, r.where)
		}
		b.WriteString("\n")
	}

	if len(hops)+len(danglingHops)+len(dangles) > 0 {
		fmt.Fprintf(&b, "### Blast radius (callers in head, depth ≤%d)\n\n", DiffDepth)
		var lines []string
		for _, d := range dangles {
			lines = append(lines, fmt.Sprintf("- %s still calls removed `%s` at %s (now unresolved)", link(head.fns[d.From].Label, head.fns[d.From].Repo, d.File, d.Line), d.Target, loc(d.File, d.Line)))
		}
		listed := map[string]bool{}
		for _, list := range [][]query.Hop{hops, danglingHops} {
			for _, h := range list {
				if listed[h.ID] || seedSet[h.ID] {
					continue
				}
				listed[h.ID] = true
				via := head.fns[h.Via].Label
				if via == "" {
					via = h.Via
				}
				l := fmt.Sprintf("- d%d %s → `%s` [%s]", h.Depth, link(h.Label, h.Repo, h.File, h.Line), via, h.Precision)
				if h.Test {
					l += " · test"
				}
				if h.Repo != rootRepo[h.ID] {
					l += " · ⇄ cross-repo"
				}
				lines = append(lines, l)
			}
		}
		for i, l := range lines {
			if i == maxListItems {
				fmt.Fprintf(&b, "- …+%d more (`cartographer impact <fn> --snapshot %s`)\n", len(lines)-i, headID)
				break
			}
			b.WriteString(l + "\n")
		}
		b.WriteString("\n")
	}

	if len(newRefs) > 0 {
		b.WriteString("### New table references [candidate]\n\n")
		for i, r := range newRefs {
			if i == maxListItems {
				fmt.Fprintf(&b, "- …+%d more\n", len(newRefs)-i)
				break
			}
			who := r.Repo + " " + loc(r.File, r.Line)
			if f, ok := head.fns[r.Function]; ok {
				who = link(f.Label, f.Repo, r.File, r.Line)
			}
			fmt.Fprintf(&b, "- `%s` ← %s\n", r.Table, who)
		}
		b.WriteString("\n")
	}

	// Diagram: changed nodes and edge endpoints, then one hop over the union.
	class := map[string]string{}
	var focus []string
	mark := func(id, c string) {
		if _, ok := class[id]; !ok {
			class[id] = c
			focus = append(focus, id)
		}
	}
	for _, sc := range sigs {
		for _, id := range sc.HeadIDs {
			mark(id, "changed")
		}
		for _, id := range sc.BaseIDs {
			if _, ok := head.fns[id]; !ok {
				mark(id, "removed")
			}
		}
	}
	for _, f := range body {
		mark(f.ID, "changed")
	}
	for _, f := range added {
		mark(f.ID, "added")
	}
	for _, f := range removed {
		mark(f.ID, "removed")
	}
	changedEdge := map[pair]bool{}
	var edgeEnds []string
	for _, list := range [][]pair{addedEdges, removedEdges} {
		for _, p := range list {
			changedEdge[p] = true
			edgeEnds = append(edgeEnds, p.From, p.To)
		}
	}
	if len(focus)+len(edgeEnds) > 0 {
		union := map[pair]bool{}
		for p := range head.edges {
			union[p] = true
		}
		for p := range base.edges {
			union[p] = true
		}
		var all []pair
		for p := range union {
			all = append(all, p)
		}
		byPair(all)
		focusSet := map[string]bool{}
		for _, id := range focus {
			focusSet[id] = true
		}
		nodeIDs := append([]string{}, focus...)
		inNodes := map[string]bool{}
		for _, id := range focus {
			inNodes[id] = true
		}
		var hop []string
		for _, p := range all {
			for _, end := range [][2]string{{p.From, p.To}, {p.To, p.From}} {
				if focusSet[end[0]] && !inNodes[end[1]] && end[1] != end[0] {
					inNodes[end[1]] = true
					hop = append(hop, end[1])
				}
			}
		}
		for _, id := range edgeEnds { // endpoints of changed calls, not expanded
			if !inNodes[id] {
				inNodes[id] = true
				hop = append(hop, id)
			}
		}
		sort.Strings(hop)
		nodeIDs = append(nodeIDs, hop...)
		hidden := 0
		if len(nodeIDs) > DefaultMaxNodes {
			hidden = len(nodeIDs) - DefaultMaxNodes
			nodeIDs = nodeIDs[:DefaultMaxNodes]
		}
		keep := map[string]bool{}
		var nodes []mnode
		for _, id := range nodeIDs {
			keep[id] = true
			f, ok := head.fns[id]
			if !ok {
				f = base.fns[id]
			}
			label := f.Label
			if label == "" {
				label = id
			}
			nodes = append(nodes, mnode{ID: id, Label: label, Repo: f.Repo, Class: class[id]})
		}
		var links []mlink
		for _, p := range all {
			if !keep[p.From] || !keep[p.To] || p.From == p.To || (!focusSet[p.From] && !focusSet[p.To] && !changedEdge[p]) {
				continue
			}
			if pr, ok := head.edges[p]; ok {
				links = append(links, mlink{From: p.From, To: p.To, Label: edgeLabel(0, pr), Dotted: pr != "precise" && pr != ""})
			} else {
				links = append(links, mlink{From: p.From, To: p.To, Label: "removed", Dotted: true, Style: "stroke:#d73a49,stroke-width:2px,stroke-dasharray:4 3"})
			}
		}
		more := ""
		if hidden > 0 {
			more = fmt.Sprintf("+%d more → cartographer impact <fn> --snapshot %s", hidden, headID)
		}
		b.WriteString("### Call graph\n\nChanged functions and one hop. Dotted red arrows are calls that exist only in base.\n\n```mermaid\n")
		writeMermaid(&b, "", nodes, links, more)
		b.WriteString("```\n\n")
	}

	stamp := func(f *snapFacts) string {
		var parts []string
		for _, r := range f.repos {
			parts = append(parts, r.Name+"@"+query.Short(r.Revision))
		}
		return "<code>" + f.id + "</code> " + strings.Join(parts, " ")
	}
	fmt.Fprintf(&b, "<sub>base %s → head %s · cartographer diff</sub>\n", stamp(base), stamp(head))
	return capChars(b.String()), nil
}

type dangling struct {
	From, Target, File string
	Line               int
}

// danglingCalls returns head call sites that are now unresolved because of
// the change: the same caller made the same call (by target text) resolved
// in base, which covers local calls resolved via the caller's module and
// callable arities lost to a dropped default argument; or the call names a
// removed function, qualified or as a local call from its module.
func danglingCalls(ctx context.Context, db *sql.DB, baseID, headID string, head *snapFacts, gone []dfn) ([]dangling, error) {
	wasResolved := map[[2]string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT from_id, target FROM calls WHERE snapshot_id = ? AND to_id IS NOT NULL AND target != ''`, baseID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			rows.Close()
			return nil, err
		}
		wasResolved[k] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	qualified, local := map[string]bool{}, map[string]bool{}
	for _, g := range gone {
		qualified[g.Label] = true
		local[g.Module+"|"+fmt.Sprintf("%s/%d", g.Name, g.Arity)] = true
	}
	rows, err = db.QueryContext(ctx, `SELECT from_id, target, kind, file, line FROM calls WHERE snapshot_id = ? AND to_id IS NULL AND target != '' ORDER BY from_id, line, target`, headID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dangling
	for rows.Next() {
		var d dangling
		var kind string
		if err := rows.Scan(&d.From, &d.Target, &kind, &d.File, &d.Line); err != nil {
			return nil, err
		}
		resolvable := kind != "dynamic" && !strings.HasPrefix(kind, "js_")
		isLocal := !strings.Contains(d.Target, ".")
		switch {
		case wasResolved[[2]string{d.From, d.Target}],
			qualified[d.Target],
			resolvable && isLocal && local[head.fns[d.From].Module+"|"+d.Target]:
			out = append(out, d)
		}
	}
	return out, rows.Err()
}
