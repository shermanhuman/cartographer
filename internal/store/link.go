package store

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/facts"
)

// link stores services with the repo each deployment builds from, then links
// HTTP refs to routes. A same-origin ref (a browser request with no host)
// links to its own repo's routes, heuristic; every other ref links to routes
// in other repos. A link is never precise: it is
// heuristic when a URL in the caller's own configuration (its config files,
// or the env of a deployment built from it) resolves through a Service to
// the route's repo, and candidate when, without that, exactly one other repo
// has a matching route. Data flow isn't traced: the evidence is repo-level.
func (im *importer) link() error {
	repos := map[string]bool{}
	for _, r := range im.b.Manifest.Repos {
		repos[r.Name] = true
	}
	imageRepo := func(image string) string {
		name := image
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if i := strings.IndexAny(name, ":@"); i >= 0 {
			name = name[:i]
		}
		if r, ok := im.b.Manifest.Images[name]; ok {
			return r
		}
		if repos[name] {
			return name
		}
		return ""
	}
	type deployment struct {
		serviceRow
		target string
	}
	var deps []deployment
	for _, sv := range im.services {
		target := ""
		if sv.Kind == "deployment" {
			target = imageRepo(sv.Image)
			deps = append(deps, deployment{sv, target})
		}
		if err := im.exec(`INSERT INTO services(snapshot_id, repo, kind, name, namespace, image, target_repo, selector, labels, env, file, line) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			im.sid, sv.repo, sv.Kind, sv.Name, ns(sv.Namespace), sv.Image, target, js(nonNilMap(sv.Selector)), js(nonNilMap(sv.Labels)), js(nonNilMap(sv.Env)), sv.File, sv.Line); err != nil {
			return err
		}
	}
	// Service (name, namespace) -> repos its selector reaches.
	svcRepos := map[[2]string]map[string]bool{}
	for _, sv := range im.services {
		if sv.Kind != "service" || len(sv.Selector) == 0 {
			continue
		}
		key := [2]string{sv.Name, ns(sv.Namespace)}
		for _, d := range deps {
			if d.target == "" || ns(d.Namespace) != key[1] || !subset(sv.Selector, d.Labels) {
				continue
			}
			if svcRepos[key] == nil {
				svcRepos[key] = map[string]bool{}
			}
			svcRepos[key][d.target] = true
		}
	}

	// The caller's evidence: its config URLs and its deployments' env URLs.
	type evidence struct {
		u          *url.URL
		provenance string
	}
	callerURLs := map[string][]evidence{}
	addURL := func(repo, value, prov string) {
		u, err := url.Parse(strings.TrimSpace(value))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return
		}
		callerURLs[repo] = append(callerURLs[repo], evidence{u, prov})
	}
	for repo, list := range im.urls {
		for _, c := range list {
			addURL(repo, c.Value, fmt.Sprintf("%s %s", c.File, c.Key))
		}
	}
	for _, d := range deps {
		if d.target == "" {
			continue
		}
		keys := make([]string, 0, len(d.Env))
		for k := range d.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			addURL(d.target, d.Env[k], fmt.Sprintf("%s:%s deployment %s env %s", d.repo, d.File, d.Name, k))
		}
	}
	resolve := func(u *url.URL) map[string]bool {
		host := u.Hostname()
		parts := strings.Split(host, ".")
		name, space := parts[0], "default"
		switch {
		case len(parts) == 1:
		case len(parts) >= 2 && (len(parts) == 2 || parts[2] == "svc"):
			space = parts[1]
		default:
			return nil // an outside host
		}
		return svcRepos[[2]string{name, space}]
	}

	byRepo := map[string][]routeRow{}
	for _, r := range im.routes {
		byRepo[r.repo] = append(byRepo[r.repo], r)
	}
	repoNames := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repoNames = append(repoNames, r)
	}
	sort.Strings(repoNames)
	matches := func(repo string, ref facts.HTTPRef, bases []string) []routeRow {
		var out []routeRow
		for _, r := range byRepo[repo] {
			if ref.Method != "" && r.Method != "" && ref.Method != r.Method {
				continue
			}
			for _, b := range bases {
				if pathMatch(r.Path, strings.TrimRight(b, "/")+ref.Path, goSubtree(r.Route)) {
					out = append(out, r)
					break
				}
			}
		}
		return out
	}
	insert := func(ref refRow, r routeRow, precision, prov string) error {
		return im.exec(`INSERT INTO http_links(snapshot_id, from_id, route_id, repo, precision, provenance, file, line) VALUES (?,?,?,?,?,?,?,?)`,
			im.sid, ref.From, r.ID, ref.repo, precision, prov, ref.File, ref.Line)
	}
	// The project's own hosts: every host its config files and its
	// deployments' env name. A request to one of them is never external.
	own := map[string]bool{}
	namespaces := map[string]bool{"default": true}
	for _, sv := range im.services {
		namespaces[ns(sv.Namespace)] = true
	}
	for _, list := range callerURLs {
		for _, ev := range list {
			own[strings.ToLower(ev.u.Hostname())] = true
		}
	}
	for _, ref := range im.refs {
		// A request to an outside service (decision 17) is external: it is
		// never matched against the project's routes.
		if ref.Origin != "same" && externalBase(ref.Base, own, namespaces) {
			if err := im.exec(`UPDATE http_refs SET origin = 'external' WHERE snapshot_id = ? AND repo = ? AND from_id = ? AND file = ? AND line = ? AND path = ?`,
				im.sid, ref.repo, ref.From, ref.File, ref.Line, ref.Path); err != nil {
				return err
			}
			continue
		}
		// A browser request with no host goes to the server that served
		// the page: the caller's own repo, and only it.
		if ref.Origin == "same" {
			for _, r := range matches(ref.repo, ref.HTTPRef, []string{""}) {
				if err := insert(ref, r, facts.Heuristic, "same-origin"); err != nil {
					return err
				}
			}
			continue
		}
		linked := false
		allBases := []string{""}
		for _, ev := range callerURLs[ref.repo] {
			allBases = append(allBases, ev.u.Path)
			targets := resolve(ev.u)
			for _, repo := range repoNames {
				if repo == ref.repo || !targets[repo] {
					continue
				}
				for _, r := range matches(repo, ref.HTTPRef, []string{"", ev.u.Path}) {
					if err := insert(ref, r, facts.Heuristic, ev.provenance+" -> "+repo); err != nil {
						return err
					}
					linked = true
				}
			}
			if linked {
				break
			}
		}
		if linked {
			continue
		}
		var only string
		var found []routeRow
		for _, repo := range repoNames {
			if repo == ref.repo {
				continue
			}
			if m := matches(repo, ref.HTTPRef, allBases); len(m) > 0 {
				if only != "" {
					found = nil // two repos match: no link
					break
				}
				only, found = repo, m
			}
		}
		for _, r := range found {
			if err := insert(ref, r, facts.Candidate, "path match, only in "+only); err != nil {
				return err
			}
		}
	}
	return nil
}

func ns(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func subset(sel, labels map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// pathMatch matches a route pattern against a request path. In the route,
// :x and {x} match one segment; *x (Phoenix, axum) and {x...} / {*x} match
// the rest, which may be empty; and with subtree (a Go pattern ending in
// /, other than "/" itself, which would match everything) the route is a
// prefix. In the request, a :x segment (an interpolation) matches only a
// route parameter. The query string is ignored.
func pathMatch(route, req string, subtree bool) bool {
	if i := strings.IndexByte(req, '?'); i >= 0 {
		req = req[:i]
	}
	// Go's {$} anchors the end of a pattern; the segment itself matches nothing.
	anchored := strings.HasSuffix(route, "{$}")
	route = strings.TrimSuffix(route, "{$}")
	subtree = subtree && !anchored && route != "/" && strings.HasSuffix(route, "/")
	rs := strings.Split(strings.Trim(route, "/"), "/")
	qs := strings.Split(strings.Trim(req, "/"), "/")
	if len(qs) == 1 && qs[0] == "" {
		qs = nil // "/" has no segments
	}
	if len(rs) == 1 && rs[0] == "" {
		rs = nil
	}
	for i, seg := range rs {
		if strings.HasPrefix(seg, "*") || strings.HasPrefix(seg, "{*") || strings.HasSuffix(seg, "...}") {
			return i <= len(qs)
		}
		if i >= len(qs) {
			return false
		}
		if strings.HasPrefix(seg, ":") || (strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")) {
			continue
		}
		if strings.HasPrefix(qs[i], ":") || seg != qs[i] {
			return false
		}
	}
	if subtree {
		return len(qs) >= len(rs)
	}
	return len(rs) == len(qs)
}

// goSubtree reports a Go route, whose pattern ending in / is a subtree.
func goSubtree(r facts.Route) bool { return strings.HasSuffix(r.File, ".go") }

// sharedSpace is 100.64.0.0/10 (RFC 6598), the carrier-grade NAT range
// Tailscale addresses come from: the project's own network.
var sharedSpace = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// externalBase reports whether a request's base names an outside service:
// an absolute http(s) URL with a literal host that is not loopback, not a
// cluster service name (one label, name.namespace for a namespace the
// project deploys to, or name.namespace.svc…), and not a host the
// project's own config or deployment env names.
func externalBase(base string, own, namespaces map[string]bool) bool {
	if strings.ContainsAny(base, "{}$#") {
		return false // interpolated: the host isn't known
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || host == "" {
		return false
	}
	// An IP address: loopback, private and link-local ones are the
	// project's own network, never an outside service.
	if ip := net.ParseIP(host); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || sharedSpace.Contains(ip)) && !own[host]
	}
	parts := strings.Split(host, ".")
	switch {
	case len(parts) == 1:
		return false
	case len(parts) == 2 && namespaces[parts[1]]:
		return false
	case len(parts) >= 3 && parts[2] == "svc":
		return false
	}
	return !own[host]
}
