package store

import (
	"fmt"
	"slices"
	"sort"

	"github.com/shermanhuman/cartographer/internal/facts"
)

// v3 stores a document's LiveView events. A send the extractor resolved (a
// template's phx-* attribute) links now, to each of its To functions; a
// hook's pushEvent names hooks instead and waits for linkEvents, since the
// hook's mounts are in another document of the same repo.
func (im *importer) v3(d *facts.Doc) error {
	if im.handlers == nil {
		im.handlers = map[string][]facts.EventHandler{}
		im.mounts = map[string][]mountRow{}
	}
	for _, h := range d.EventHandlers {
		if err := im.exec(`INSERT INTO event_handlers(snapshot_id, repo, function_id, event, source, delegate_id, line) VALUES (?,?,?,?,?,?,?)`,
			im.sid, d.Repo, h.Function, h.Event, h.Source, h.Delegate, h.Line); err != nil {
			return err
		}
		im.handlers[h.Function] = append(im.handlers[h.Function], h)
	}
	for _, m := range d.HookMounts {
		if err := im.exec(`INSERT INTO hook_mounts(snapshot_id, repo, hook, from_id, receivers, file, line) VALUES (?,?,?,?,?,?,?)`,
			im.sid, d.Repo, m.Hook, m.From, js(nonNil(m.Receivers)), m.File, m.Line); err != nil {
			return err
		}
		im.mounts[d.Repo] = append(im.mounts[d.Repo], mountRow{m, d.Repo})
	}
	for _, e := range d.Events {
		dyn := 0
		if e.Dynamic {
			dyn = 1
		}
		if err := im.exec(`INSERT INTO event_sends(snapshot_id, repo, from_id, event, dynamic, via, target, hooks, file, line) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			im.sid, d.Repo, e.From, e.Event, dyn, e.Via, e.Target, js(nonNil(e.Hooks)), e.File, e.Line); err != nil {
			return err
		}
		if len(e.Hooks) > 0 && len(e.To) == 0 {
			im.sends = append(im.sends, sendRow{e, d.Repo})
		}
	}
	// To is filled once the document's handlers are known (they may come
	// after the events in another order of documents, but not of sections).
	for _, e := range d.Events {
		for _, to := range e.To {
			if err := im.insertEventLink(e, to, d.Repo, e.Precision, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// clauseLine is the first clause of fn that takes event (a delegating
// catch-all when no clause names it).
func (im *importer) clauseLine(fn, event string) int {
	line := 0
	for _, h := range im.handlers[fn] {
		if h.Event == event {
			return h.Line
		}
		if h.Source == "delegate" && line == 0 {
			line = h.Line
		}
	}
	return line
}

func (im *importer) insertEventLink(e facts.Event, to, repo, precision, provenance string) error {
	if precision == "" {
		precision = facts.Heuristic
	}
	return im.exec(`INSERT INTO event_links(snapshot_id, from_id, to_id, event, via, repo, precision, provenance, file, line, to_line) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		im.sid, e.From, to, e.Event, e.Via, repo, precision, provenance, e.File, e.Line, im.clauseLine(to, e.Event))
}

// takes reports whether handle_event function fn has a clause for event,
// and the delegates (one hop) that take it.
func (im *importer) takes(fn, event string) (bool, []string) {
	direct := false
	var delegates []string
	for _, h := range im.handlers[fn] {
		switch {
		case h.Event == event:
			direct = true
		case h.Source == "delegate":
			for _, dh := range im.handlers[h.Delegate] {
				if dh.Event == event && !slices.Contains(delegates, h.Delegate) {
					delegates = append(delegates, h.Delegate)
				}
			}
		}
	}
	return direct, delegates
}

// linkEvents links a hook's pushEvent to the handle_event of each LiveView
// that mounts the hook (phx-hook, in the same repo) and has a clause for the
// event. Each link is heuristic: the hook object's registry names come from
// `new LiveSocket(..., {hooks})`, and the receivers from the mount's
// template. A dynamic name, or a hook no template mounts, links nothing.
func (im *importer) linkEvents() error {
	for _, s := range im.sends {
		if s.Dynamic {
			continue
		}
		type target struct{ to, prov string }
		var out []target
		for _, m := range im.mounts[s.repo] {
			if !slices.Contains(s.Hooks, m.Hook) {
				continue
			}
			prov := fmt.Sprintf("phx-hook %s (%s:%d)", m.Hook, m.File, m.Line)
			for _, r := range m.Receivers {
				direct, delegates := im.takes(r, s.Event.Event)
				if direct || len(delegates) > 0 {
					out = append(out, target{r, prov})
				}
				for _, dl := range delegates {
					out = append(out, target{dl, prov})
				}
			}
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].to < out[j].to || out[i].to == out[j].to && out[i].prov < out[j].prov
		})
		seen := map[string]bool{}
		for _, t := range out {
			if seen[t.to] {
				continue
			}
			seen[t.to] = true
			if err := im.insertEventLink(s.Event, t.to, s.repo, facts.Heuristic, t.prov); err != nil {
				return err
			}
		}
	}
	return nil
}
