package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Review records the current fingerprints of everything annotation id (and
// its steps) cites, using snapshot sid in db. Unknown fields in the
// annotation file are preserved. It returns the number of citations stamped.
func Review(ctx context.Context, c *Config, db *sql.DB, sid, id string) (int, error) {
	files, _ := filepath.Glob(filepath.Join(c.dir, c.Annotations, "*.json"))
	sort.Strings(files)
	fp := func(cite string) (string, bool) {
		var f string
		err := db.QueryRowContext(ctx, `SELECT fingerprint FROM functions WHERE snapshot_id = ? AND id = ?`, sid, cite).Scan(&f)
		return f, err == nil && f != ""
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
		list, _ := doc["annotations"].([]any)
		for _, item := range list {
			a, _ := item.(map[string]any)
			if a == nil || a["id"] != id {
				continue
			}
			n := 0
			stamp := func(node map[string]any) {
				cites, _ := node["cites"].([]any)
				reviewed := map[string]any{}
				for _, ci := range cites {
					s, _ := ci.(string)
					if f, ok := fp(s); ok {
						reviewed[s] = f
						n++
					}
				}
				if len(reviewed) > 0 {
					node["reviewed"] = reviewed
				} else {
					delete(node, "reviewed")
				}
			}
			stamp(a)
			steps, _ := a["steps"].([]any)
			for _, st := range steps {
				if m, ok := st.(map[string]any); ok {
					stamp(m)
				}
			}
			out, _ := json.MarshalIndent(doc, "", "  ")
			return n, os.WriteFile(path, append(out, '\n'), 0o644)
		}
	}
	return 0, fmt.Errorf("annotation %q not found in %s", id, filepath.Join(c.dir, c.Annotations))
}
