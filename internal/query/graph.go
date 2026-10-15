package query

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Exported graph primitives for the renderers, the HTTP API and exports.

// Edge is an aggregated caller->callee relationship (all sites of one pair).
type Edge = edge

// Hop is one node reached by a call-graph walk.
type Hop = hop

// Get returns the entity with exactly this id, or nil.
func (q *Q) Get(ctx context.Context, id string) (*Entity, error) { return q.get(ctx, id) }

// Edges returns aggregated resolved calls out of ids (or into ids when reverse).
func (q *Q) Edges(ctx context.Context, ids []string, reverse bool) ([]Edge, error) {
	return q.edges(ctx, ids, reverse)
}

// Walk does a breadth-first walk over resolved calls from start, up to depth.
func (q *Q) Walk(ctx context.Context, start []string, depth int, reverse bool) ([]Hop, error) {
	return q.walk(ctx, start, depth, reverse)
}

// FunctionsOf expands an entity (function, module, table, context, file)
// into the function ids it stands for, with a precision note for tables.
func (q *Q) FunctionsOf(ctx context.Context, e *Entity) ([]string, string, error) {
	return q.functionsOf(ctx, e)
}

// Repo is one repository of the snapshot.
type Repo struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Revision string `json:"revision"`
}

// Repos lists the snapshot's repositories by name.
func (q *Q) Repos(ctx context.Context) ([]Repo, error) {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT name, url, revision FROM repos WHERE snapshot_id = ? ORDER BY name`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.Name, &r.URL, &r.Revision); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Provenance is the one-line "map <snapshot> · repo@sha …" stamp every
// result, embed and export carries.
func (q *Q) Provenance(ctx context.Context) string {
	repos, _ := q.Repos(ctx)
	line := "map " + q.Snapshot
	var parts []string
	for _, r := range repos {
		parts = append(parts, r.Name+"@"+Short(r.Revision))
	}
	if len(parts) > 0 {
		line += " · " + strings.Join(parts, " ")
	}
	return line
}

// Short abbreviates a revision to 7 characters.
func Short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// BlobURL links a file (and line) at the repository's pinned revision, or ""
// when the repository has no URL.
func BlobURL(r Repo, file string, line int) string {
	if r.URL == "" || file == "" {
		return ""
	}
	rev := r.Revision
	if rev == "" {
		rev = "HEAD"
	}
	segs := strings.Split(file, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u := strings.TrimSuffix(strings.TrimSuffix(r.URL, "/"), ".git") + "/blob/" + rev + "/" + strings.Join(segs, "/")
	if line > 0 {
		u += "#L" + strconv.Itoa(line)
	}
	return u
}
