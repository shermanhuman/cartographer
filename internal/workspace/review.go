package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Review records the current fingerprints of everything annotation id (and
// its steps) cites, using snapshot sid in db. Only the reviewed stamps are
// rewritten: keys keep their authored order and values (strings, numbers,
// unknown fields) are written back verbatim, re-indented with two spaces.
// It returns the number of citations stamped.
func Review(ctx context.Context, c *Config, db *sql.DB, sid, id string) (int, error) {
	files, _ := filepath.Glob(filepath.Join(c.dir, c.Annotations, "*.json"))
	sort.Strings(files)
	fp := func(cite string) (string, bool) {
		var f string
		err := db.QueryRowContext(ctx, `SELECT fingerprint FROM functions WHERE snapshot_id = ? AND id = ?`, sid, cite).Scan(&f)
		return f, err == nil && f != ""
	}
	// stamp replaces node's reviewed object with the fingerprints of its
	// cites, in cite order, and returns how many it stamped.
	stamp := func(node *object) int {
		var cites []any
		json.Unmarshal(node.vals["cites"], &cites)
		reviewed := newObject()
		for _, ci := range cites {
			s, _ := ci.(string)
			if f, ok := fp(s); ok {
				reviewed.set(s, marshal(f))
			}
		}
		if len(reviewed.keys) > 0 {
			node.set("reviewed", reviewed.raw())
		} else {
			node.del("reviewed")
		}
		return len(reviewed.keys)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		doc, err := parseObject(raw)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
		var list []json.RawMessage
		json.Unmarshal(doc.vals["annotations"], &list)
		for i, item := range list {
			a, err := parseObject(item)
			if err != nil {
				continue
			}
			var aid string
			json.Unmarshal(a.vals["id"], &aid)
			if aid != id {
				continue
			}
			n := stamp(a)
			var steps []json.RawMessage
			if json.Unmarshal(a.vals["steps"], &steps) == nil && steps != nil {
				for j, st := range steps {
					if m, err := parseObject(st); err == nil {
						n += stamp(m)
						steps[j] = m.raw()
					}
				}
				a.set("steps", rawArray(steps))
			}
			list[i] = a.raw()
			doc.set("annotations", rawArray(list))
			var out bytes.Buffer
			if err := json.Indent(&out, doc.raw(), "", "  "); err != nil {
				return 0, err
			}
			out.WriteByte('\n')
			return n, writeAtomic(path, out.Bytes())
		}
	}
	return 0, fmt.Errorf("annotation %q not found in %s", id, filepath.Join(c.dir, c.Annotations))
}

// object is a JSON object that keeps its keys in authored order. Values stay
// raw, so they are written back exactly as read (json.Indent copies strings
// and numbers verbatim).
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func newObject() *object { return &object{vals: map[string]json.RawMessage{}} }

func parseObject(raw []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	o := newObject()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o.set(k, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return o, nil
}

// set replaces k's value in place, or appends k.
func (o *object) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *object) raw() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(marshal(k))
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func rawArray(items []json.RawMessage) json.RawMessage {
	b := []byte{'['}
	for i, it := range items {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, it...)
	}
	return append(b, ']')
}

// marshal encodes v without escaping <, > and &.
func marshal(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}
