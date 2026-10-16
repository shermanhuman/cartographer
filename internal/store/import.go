package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shermanhuman/cartographer/internal/bundle"
	"github.com/shermanhuman/cartographer/internal/facts"
	"github.com/shermanhuman/cartographer/internal/ident"
)

// Import loads a snapshot directory, replacing any snapshot of the same
// workspace with the same id.
func (s *Store) Import(ctx context.Context, dir string) (string, error) {
	b, err := bundle.Read(dir)
	if err != nil {
		return "", err
	}
	return b.Manifest.ID, s.ImportBundle(ctx, b)
}

// ImportBundle loads an already-read bundle.
func (s *Store) ImportBundle(ctx context.Context, b *bundle.Bundle) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	im := &importer{ctx: ctx, tx: tx, b: b, sid: b.Manifest.ID, stmts: map[string]*sql.Stmt{}}
	defer im.closeStmts()
	// Ids are unique per workspace; never replace another workspace's graph.
	var ws string
	switch err := tx.QueryRowContext(ctx, `SELECT workspace FROM snapshots WHERE id = ?`, im.sid).Scan(&ws); {
	case err == nil && ws != b.Manifest.Workspace:
		return fmt.Errorf("store: import %s: snapshot %s belongs to workspace %q, not %q", im.sid, im.sid, ws, b.Manifest.Workspace)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if err := deleteSnapshot(ctx, tx, im.sid); err != nil {
		return err
	}
	if err := im.run(); err != nil {
		return fmt.Errorf("store: import %s: %w", im.sid, err)
	}
	return tx.Commit()
}

type importer struct {
	ctx   context.Context
	tx    *sql.Tx
	b     *bundle.Bundle
	sid   string
	stmts map[string]*sql.Stmt

	funcs     map[string]facts.Function // id -> function
	fnRepo    map[string]string         // function id -> repo
	modules   map[string]facts.Module   // id -> module
	modRepo   map[string]string
	vocab     map[string]int
	seenFiles map[string]bool
	seenCtx   map[string]bool // context ids, unique across facts documents
}

func (im *importer) exec(query string, args ...any) error {
	st, ok := im.stmts[query]
	if !ok {
		var err error
		if st, err = im.tx.PrepareContext(im.ctx, query); err != nil {
			return err
		}
		im.stmts[query] = st
	}
	_, err := st.ExecContext(im.ctx, args...)
	return err
}

func (im *importer) closeStmts() {
	for _, st := range im.stmts {
		st.Close()
	}
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (im *importer) run() error {
	m := im.b.Manifest
	if err := im.exec(`INSERT INTO snapshots(id, workspace, created_at, imported_at, manifest) VALUES (?,?,?,?,?)`,
		m.ID, m.Workspace, m.CreatedAt, time.Now().UTC().Format(time.RFC3339), js(m)); err != nil {
		return err
	}
	for _, r := range m.Repos {
		if err := im.exec(`INSERT INTO repos(snapshot_id, name, url, revision) VALUES (?,?,?,?)`, im.sid, r.Name, r.URL, r.Revision); err != nil {
			return err
		}
	}

	im.funcs, im.fnRepo = map[string]facts.Function{}, map[string]string{}
	im.modules, im.modRepo = map[string]facts.Module{}, map[string]string{}
	im.vocab = map[string]int{}
	im.seenFiles = map[string]bool{}
	im.seenCtx = map[string]bool{}

	for _, d := range im.b.Facts {
		if err := im.definitions(d); err != nil {
			return err
		}
	}
	idx := im.targetIndex()
	for _, d := range im.b.Facts {
		if err := im.calls(d, idx); err != nil {
			return err
		}
		if err := im.tableRefs(d); err != nil {
			return err
		}
		for _, n := range d.Notes {
			if err := im.exec(`INSERT INTO notes(snapshot_id, repo, kind, file, message) VALUES (?,?,?,?,?)`, im.sid, d.Repo, n.Kind, n.File, n.Message); err != nil {
				return err
			}
		}
	}
	if err := im.docs(); err != nil {
		return err
	}
	if err := im.annotations(); err != nil {
		return err
	}
	for w, n := range im.vocab {
		if err := im.exec(`INSERT INTO vocab(snapshot_id, word, count) VALUES (?,?,?)`, im.sid, w, n); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) entity(id, kind, label string, extra ...string) error {
	words := ident.Words(label)
	for _, e := range extra {
		words = append(words, ident.Words(e)...)
	}
	for _, w := range words {
		im.vocab[w]++
	}
	return im.exec(`INSERT INTO entities(snapshot_id, id, kind, label, words) VALUES (?,?,?,?,?)`,
		im.sid, id, kind, label, strings.Join(words, " "))
}

func (im *importer) definitions(d *facts.Doc) error {
	repo := d.Repo
	for _, f := range d.Files {
		key := repo + "|" + f.Path
		if im.seenFiles[key] {
			continue // several plugins may list the same file
		}
		im.seenFiles[key] = true
		if err := im.exec(`INSERT OR IGNORE INTO files(snapshot_id, repo, path, language, category) VALUES (?,?,?,?,?)`, im.sid, repo, f.Path, f.Language, f.Category); err != nil {
			return err
		}
		if err := im.entity("file:"+repo+":"+f.Path, "file", f.Path); err != nil {
			return err
		}
	}
	depth := im.b.Manifest.Contexts.Depth
	contexts := map[string]string{}
	for _, mo := range d.Modules {
		ctxName := contextName(mo, depth)
		ctxID := "ctx:" + repo + ":" + ctxName
		if !im.seenCtx[ctxID] { // several plugins may fill one context
			im.seenCtx[ctxID] = true
			contexts[ctxID] = ctxName
		}
		im.modules[mo.ID], im.modRepo[mo.ID] = mo, repo
		if err := im.exec(`INSERT INTO modules(snapshot_id, id, repo, name, file, line, language, context) VALUES (?,?,?,?,?,?,?,?)`,
			im.sid, mo.ID, repo, mo.Name, mo.File, mo.Line, mo.Language, ctxID); err != nil {
			return err
		}
		if err := im.entity(mo.ID, "module", mo.Name); err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(contexts))
	for id := range contexts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := im.exec(`INSERT INTO contexts(snapshot_id, id, repo, name) VALUES (?,?,?,?)`, im.sid, id, repo, contexts[id]); err != nil {
			return err
		}
		if err := im.entity(id, "context", contexts[id]); err != nil {
			return err
		}
	}
	for _, f := range d.Functions {
		// Bundles are validated on read; targetIndex enumerates the range.
		if f.MinArity < 0 || f.MinArity > f.Arity || f.Arity > facts.MaxArity {
			return fmt.Errorf("function %s: arity range %d..%d out of bounds (0..%d)", f.ID, f.MinArity, f.Arity, facts.MaxArity)
		}
		im.funcs[f.ID], im.fnRepo[f.ID] = f, repo
		if err := im.exec(`INSERT INTO functions(snapshot_id, id, repo, module, name, arity, min_arity, kind, file, line, clauses, fingerprint) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			im.sid, f.ID, repo, f.Module, f.Name, f.Arity, f.MinArity, f.Kind, f.File, f.Line, js(nonNil(f.Clauses)), f.Fingerprint); err != nil {
			return err
		}
		if err := im.entity(f.ID, "function", label(f, im.modules)); err != nil {
			return err
		}
	}
	for _, t := range d.Tables {
		kind := t.Kind
		if kind == "" {
			kind = "table"
		}
		if err := im.exec(`INSERT INTO db_tables(snapshot_id, id, schema_name, name, kind, columns, constraints, indexes) VALUES (?,?,?,?,?,?,?,?)`,
			im.sid, t.ID, t.Schema, t.Name, kind, js(nonNil(t.Columns)), js(nonNil(t.Constraints)), js(nonNil(t.Indexes))); err != nil {
			return err
		}
		cols := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			cols[i] = c.Name
		}
		if err := im.entity(t.ID, "table", t.ID, cols...); err != nil {
			return err
		}
	}
	for _, fk := range d.ForeignKeys {
		if err := im.exec(`INSERT INTO foreign_keys(snapshot_id, name, from_table, from_columns, to_table, to_columns) VALUES (?,?,?,?,?,?)`,
			im.sid, fk.Name, fk.FromTable, js(fk.FromColumns), fk.ToTable, js(fk.ToColumns)); err != nil {
			return err
		}
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// label renders a function as Module.name/arity.
func label(f facts.Function, modules map[string]facts.Module) string {
	if mo, ok := modules[f.Module]; ok {
		return fmt.Sprintf("%s.%s/%d", mo.Name, f.Name, f.Arity)
	}
	return fmt.Sprintf("%s/%d", f.Name, f.Arity)
}

func contextName(mo facts.Module, depth int) string {
	ns := mo.Namespace
	if len(ns) == 0 {
		ns = strings.Split(mo.Name, ".")
	}
	if len(ns) > depth {
		ns = ns[:depth]
	}
	return strings.Join(ns, ".")
}

type targetKey struct {
	repo, module, name string
	arity              int
}

func (im *importer) calls(d *facts.Doc, idx *targetIndex) error {
	defPrecision := d.Producer.CallPrecision
	if defPrecision == "" {
		defPrecision = facts.Heuristic
	}
	for _, c := range d.Calls {
		to, precision, target := c.To, c.Precision, ""
		if c.Target != nil {
			target = fmt.Sprintf("%s.%s/%d", c.Target.Module, c.Target.Name, c.Target.Arity)
			if c.Target.Module == "" {
				target = fmt.Sprintf("%s/%d", c.Target.Name, c.Target.Arity)
			}
		}
		if to == "" && c.Target != nil && c.Kind != "dynamic" && !strings.HasPrefix(c.Kind, "js_") {
			to, precision = idx.resolve(d.Repo, im.callerModule(c.From), *c.Target, defPrecision)
		}
		if to != "" && precision == "" {
			precision = defPrecision
		}
		var toArg any
		if to != "" {
			toArg = to
		}
		if err := im.exec(`INSERT INTO calls(snapshot_id, from_id, to_id, target, kind, repo, file, line, precision) VALUES (?,?,?,?,?,?,?,?,?)`,
			im.sid, c.From, toArg, target, c.Kind, d.Repo, c.File, c.Line, precision); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) callerModule(fnID string) string {
	if f, ok := im.funcs[fnID]; ok {
		if mo, ok := im.modules[f.Module]; ok {
			return mo.Name
		}
	}
	return ""
}

type targetIndex struct {
	byRepo map[targetKey][]string
	global map[targetKey][]string // repo field empty
}

func (im *importer) targetIndex() *targetIndex {
	idx := &targetIndex{byRepo: map[targetKey][]string{}, global: map[targetKey][]string{}}
	ids := make([]string, 0, len(im.funcs))
	for id := range im.funcs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		f := im.funcs[id]
		modName := ""
		if mo, ok := im.modules[f.Module]; ok {
			modName = mo.Name
		}
		for a := f.MinArity; a <= f.Arity; a++ {
			k := targetKey{im.fnRepo[id], modName, f.Name, a}
			idx.byRepo[k] = append(idx.byRepo[k], id)
			k.repo = ""
			idx.global[k] = append(idx.global[k], id)
		}
	}
	return idx
}

// resolve finds a unique callee: same repository first (producer precision),
// then anywhere in the workspace (heuristic). Local calls without a module
// resolve against the caller's module.
func (idx *targetIndex) resolve(repo, callerModule string, t facts.Target, precision string) (string, string) {
	mod := t.Module
	if mod == "" {
		mod = callerModule
	}
	if ids := idx.byRepo[targetKey{repo, mod, t.Name, t.Arity}]; len(ids) == 1 {
		return ids[0], precision
	}
	if t.Module == "" {
		return "", ""
	}
	if ids := idx.global[targetKey{"", mod, t.Name, t.Arity}]; len(ids) == 1 {
		return ids[0], facts.Heuristic
	}
	return "", ""
}

func (im *importer) tableRefs(d *facts.Doc) error {
	for _, r := range d.TableRefs {
		if err := im.exec(`INSERT INTO table_refs(snapshot_id, repo, table_id, file, line, function_id, text) VALUES (?,?,?,?,?,?,?)`,
			im.sid, d.Repo, r.Table, r.File, r.Line, r.Function, r.Text); err != nil {
			return err
		}
	}
	return nil
}

var funcRef = regexp.MustCompile(`([A-Z][A-Za-z0-9_]*(?:\.[A-Z][A-Za-z0-9_]*)*)\.([a-z_][A-Za-z0-9_?!]*)/(\d+)`)

// docs links Markdown to the entities it mentions: Module.fun/arity, module
// names and repository paths. Matches are exact, so links are precise.
func (im *importer) docs() error {
	modByName := map[string][]string{} // name -> module ids
	for id, mo := range im.modules {
		modByName[mo.Name] = append(modByName[mo.Name], id)
	}
	names := make([]string, 0, len(modByName))
	for n := range modByName {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) }) // longest first
	fnByLabel := map[string]string{}
	for id, f := range im.funcs {
		fnByLabel[im.fnRepo[id]+"|"+label(f, im.modules)] = id
	}
	paths := map[string]bool{}
	rows, err := im.tx.QueryContext(im.ctx, `SELECT repo || '|' || path FROM files WHERE snapshot_id = ?`, im.sid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		paths[k] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, doc := range im.b.Docs {
		title := doc.Path
		lines := strings.Split(doc.Content, "\n")
		for _, l := range lines {
			if strings.HasPrefix(l, "# ") {
				title = strings.TrimSpace(l[2:])
				break
			}
		}
		if err := im.exec(`INSERT INTO docs(snapshot_id, repo, path, title) VALUES (?,?,?,?)`, im.sid, doc.Repo, doc.Path, title); err != nil {
			return err
		}
		docID := "doc:" + doc.Repo + ":" + doc.Path
		if err := im.entity(docID, "doc", title, doc.Path); err != nil {
			return err
		}
		for n, line := range lines {
			seen := map[string]bool{}
			link := func(id string) error {
				if seen[id] {
					return nil
				}
				seen[id] = true
				return im.exec(`INSERT INTO doc_refs(snapshot_id, repo, doc_path, entity_id, line) VALUES (?,?,?,?,?)`, im.sid, doc.Repo, doc.Path, id, n+1)
			}
			for _, m := range funcRef.FindAllStringSubmatch(line, -1) {
				if id, ok := fnByLabel[doc.Repo+"|"+m[0]]; ok {
					if err := link(id); err != nil {
						return err
					}
				}
			}
			for _, name := range names {
				if !containsWord(line, name) {
					continue
				}
				for _, id := range modByName[name] {
					if im.modRepo[id] == doc.Repo || len(modByName[name]) == 1 {
						if err := link(id); err != nil {
							return err
						}
					}
				}
			}
			for _, tok := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '`' || r == '(' || r == ')' || r == ',' }) {
				tok = strings.TrimRight(tok, ".:;")
				if paths[doc.Repo+"|"+tok] {
					if err := link("file:" + doc.Repo + ":" + tok); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// containsWord reports whether name occurs in s not embedded in a longer
// identifier (so "Shop.Orders" doesn't match inside "Shop.OrdersTest").
func containsWord(s, name string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], name)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(name)
		before := start == 0 || !isIdent(s[start-1])
		// "Shop.Orders.create" mentions Shop.Orders; "Shop.Orders.Line" is another module.
		after := end == len(s) ||
			(s[end] == '.' && (end+1 == len(s) || !isUpper(s[end+1]))) ||
			(s[end] != '.' && !isIdent(s[end]))
		if before && after {
			return true
		}
		i = start + 1
	}
}

func isIdent(c byte) bool {
	return c == '_' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// StepStatus is the freshness of one cited annotation step.
type StepStatus struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
}

// annotations computes Swimm-style freshness: a cited entity that is gone, or
// whose fingerprint differs from the reviewed one, makes the step stale.
// Review stamps only functions with a fingerprint, so only they can be
// unreviewed: a step citing one with no stamps at all is unreviewed, and once
// a step has been reviewed, such a function without a stamp (added later)
// needs review. Other cites (modules, tables, files, contexts) are judged on
// existence alone. Steps that cite nothing don't affect the status.
func (im *importer) annotations() error {
	for _, a := range im.b.Annotations {
		var steps []StepStatus
		if len(a.Steps) == 0 {
			st, err := im.stepStatus(a.ID, a.Cites, a.Reviewed)
			if err != nil {
				return err
			}
			steps = append(steps, st)
		}
		for _, s := range a.Steps {
			st, err := im.stepStatus(s.ID, s.Cites, s.Reviewed)
			if err != nil {
				return err
			}
			steps = append(steps, st)
		}
		overall := "reviewed"
		for _, s := range steps {
			switch {
			case s.Status == "needs_review":
				overall = "needs_review"
			case s.Status == "unreviewed" && overall == "reviewed":
				overall = "unreviewed"
			}
		}
		if err := im.exec(`INSERT INTO annotations(snapshot_id, id, kind, title, body, status, steps) VALUES (?,?,?,?,?,?,?)`,
			im.sid, a.ID, a.Kind, a.Title, rawOr(a), overall, js(steps)); err != nil {
			return err
		}
		if err := im.entity("ann:"+a.ID, "annotation", a.Title, a.Summary); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) stepStatus(id string, cites []string, reviewed map[string]string) (StepStatus, error) {
	st := StepStatus{ID: id, Status: "reviewed"}
	stamped := false
	for _, v := range reviewed {
		stamped = stamped || v != ""
	}
	for _, c := range cites {
		f, isFn := im.funcs[c]
		_, isMod := im.modules[c]
		known := isFn || isMod
		if !known {
			var err error
			if known, err = im.knownOther(c); err != nil {
				return st, err
			}
		}
		unstamped := isFn && f.Fingerprint != "" && reviewed[c] == ""
		switch {
		case !known:
			st.Status = "needs_review"
			st.Reasons = append(st.Reasons, "missing: "+c)
		case isFn && reviewed[c] != "" && reviewed[c] != f.Fingerprint:
			st.Status = "needs_review"
			st.Reasons = append(st.Reasons, "changed: "+c)
		case unstamped && stamped:
			st.Status = "needs_review"
			st.Reasons = append(st.Reasons, "unreviewed: "+c)
		case unstamped && st.Status == "reviewed":
			st.Status = "unreviewed"
		}
	}
	return st, nil
}

// knownOther reports whether id names a table, file or context in this snapshot.
func (im *importer) knownOther(id string) (bool, error) {
	var n int
	err := im.tx.QueryRowContext(im.ctx, `SELECT
	  (SELECT count(*) FROM db_tables WHERE snapshot_id = ?1 AND id = ?2) +
	  (SELECT count(*) FROM contexts WHERE snapshot_id = ?1 AND id = ?2) +
	  (SELECT count(*) FROM files WHERE snapshot_id = ?1 AND 'file:' || repo || ':' || path = ?2)`, im.sid, id).Scan(&n)
	return n > 0, err
}

func rawOr(a bundle.Annotation) string {
	if len(a.Raw) > 0 {
		return string(a.Raw)
	}
	return js(a)
}
