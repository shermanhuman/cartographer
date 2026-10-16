// Package snapshot publishes snapshots as GitHub releases and pulls them
// into the local store.
package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// TagPrefix marks releases that hold snapshots.
const TagPrefix = "snapshot-"

// AssetName is the release asset holding the packed snapshot.
const AssetName = "snapshot.tar.gz"

// Default request timeouts. API calls are small; asset transfers get longer.
const (
	DefaultTimeout         = 30 * time.Second
	DefaultTransferTimeout = 10 * time.Minute
)

// GitHub is a minimal REST client for releases.
type GitHub struct {
	API     string // default https://api.github.com
	Uploads string // default https://uploads.github.com
	Token   string
	HTTP    *http.Client
	// Timeout bounds each API call (default DefaultTimeout).
	Timeout time.Duration
	// TransferTimeout bounds an asset upload or download, including reading
	// the body (default DefaultTransferTimeout).
	TransferTimeout time.Duration
}

// defaultClient has no overall timeout (asset bodies can be large; each call
// sets a context deadline instead) but bounds connecting and waiting for
// response headers, so a dead network fails fast.
var defaultClient = func() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = DefaultTimeout
	t.TLSHandshakeTimeout = 10 * time.Second
	return &http.Client{Transport: t}
}()

// Token finds a GitHub token: $GH_TOKEN, $GITHUB_TOKEN, then `gh auth token`
// (gh on PATH, else a gh that `mise which` finds). Commands run from the
// temp directory with mise auto-install off, so the current repository's
// tool config is never installed or trusted.
func Token() string {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		out, err := neutralOutput("mise", "which", "gh")
		if err != nil {
			return ""
		}
		gh = strings.TrimSpace(string(out))
	}
	if gh == "" {
		return ""
	}
	out, err := neutralOutput(gh, "auth", "token")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// neutralOutput runs a short command from the temp directory with mise
// auto-install disabled and returns its stdout.
func neutralOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "MISE_EXEC_AUTO_INSTALL=false", "MISE_AUTO_INSTALL=false", "MISE_NOT_FOUND_AUTO_INSTALL=false")
	return cmd.Output()
}

// NewGitHub returns a client using Token().
func NewGitHub() *GitHub { return &GitHub{Token: Token()} }

func (g *GitHub) api() string {
	if g.API != "" {
		return strings.TrimRight(g.API, "/")
	}
	return "https://api.github.com"
}

func (g *GitHub) uploads() string {
	if g.Uploads != "" {
		return strings.TrimRight(g.Uploads, "/")
	}
	return "https://uploads.github.com"
}

func (g *GitHub) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return defaultClient
}

func (g *GitHub) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return DefaultTimeout
}

func (g *GitHub) transferTimeout() time.Duration {
	if g.TransferTimeout > 0 {
		return g.TransferTimeout
	}
	return DefaultTransferTimeout
}

// cancelBody cancels its request context when the body is closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// HTTPError is a non-2xx GitHub response.
type HTTPError struct {
	Method, URL string
	StatusCode  int
	Status      string
	Body        string
}

func (e *HTTPError) Error() string {
	hint := ""
	if e.StatusCode == 401 || e.StatusCode == 404 {
		hint = " (check access to the repository and the token: GH_TOKEN, GITHUB_TOKEN or `gh auth login`)"
	}
	return fmt.Sprintf("github %s %s: %s: %s%s", e.Method, e.URL, e.Status, e.Body, hint)
}

// hasStatus reports whether err is an HTTPError with one of codes.
func hasStatus(err error, codes ...int) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	for _, c := range codes {
		if he.StatusCode == c {
			return true
		}
	}
	return false
}

func (g *GitHub) do(ctx context.Context, method, u string, body io.Reader, contentType, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &HTTPError{Method: method, URL: u, StatusCode: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(msg))}
	}
	return resp, nil
}

func (g *GitHub) json(ctx context.Context, method, u string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	resp, err := g.do(ctx, method, u, body, "application/json", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Release is a snapshot release.
type Release struct {
	ID     int64   `json:"id"`
	Tag    string  `json:"tag_name"`
	Draft  bool    `json:"draft"`
	Assets []Asset `json:"assets"`
}

type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// State is "uploaded" once the upload finished; a failed upload can
	// leave an empty "starter" asset behind.
	State string `json:"state"`
	Size  int64  `json:"size"`
}

// Complete reports whether r is a published (non-draft) release holding a
// fully uploaded, non-empty snapshot asset.
func (r Release) Complete() bool {
	if r.Draft {
		return false
	}
	for _, a := range r.Assets {
		if a.Name == AssetName && a.State == "uploaded" && a.Size > 0 {
			return true
		}
	}
	return false
}

// Snapshots lists complete snapshot releases in repo (owner/name), newest
// first. Drafts and releases without the snapshot asset (an interrupted or
// in-progress publish) are skipped, so they never shadow a usable snapshot.
func (g *GitHub) Snapshots(ctx context.Context, repo string) ([]Release, error) {
	all, err := g.snapshotReleases(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range all {
		if r.Complete() {
			out = append(out, r)
		}
	}
	return out, nil
}

// snapshotReleases lists every snapshot-tagged release in repo, drafts and
// incomplete ones included, newest first. Drafts are listed only for tokens
// with push access.
func (g *GitHub) snapshotReleases(ctx context.Context, repo string) ([]Release, error) {
	var all []Release
	for page := 1; page <= 10; page++ {
		var rs []Release
		if err := g.json(ctx, "GET", fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", g.api(), repo, page), nil, &rs); err != nil {
			return nil, err
		}
		for _, r := range rs {
			if strings.HasPrefix(r.Tag, TagPrefix) {
				all = append(all, r)
			}
		}
		if len(rs) < 100 {
			break
		}
	}
	// Tags start with a fixed-width UTC timestamp (bundle.SnapshotID), so
	// lexical order is chronological.
	sort.Slice(all, func(i, j int) bool { return all[i].Tag > all[j].Tag })
	return all, nil
}

// Publish creates a draft release tagged with the snapshot id, uploads the
// asset, then publishes the release. Readers skip drafts, so a run killed
// part-way (e.g. a cancelled CI job) never leaves a published release without
// its asset. GitHub creates the tag (from the default branch) only when the
// draft is published. On failure the draft is deleted; if the publish request
// fails, the release is re-read first, and a release that is already
// published (or whose state can't be read: ErrPublishUncertain) is kept.
func (g *GitHub) Publish(ctx context.Context, repo, id, notes string, asset []byte) (*Release, error) {
	var r Release
	err := g.json(ctx, "POST", fmt.Sprintf("%s/repos/%s/releases", g.api(), repo), map[string]any{
		"tag_name": id, "name": id, "body": notes, "draft": true, "prerelease": false,
	}, &r)
	if err != nil {
		return nil, err
	}
	// Clean up even if ctx was cancelled; g.json bounds each call. A failed
	// cleanup is reported with the error that caused it.
	fail := func(err error) error {
		if cerr := g.deleteRelease(context.WithoutCancel(ctx), repo, r); cerr != nil {
			return errors.Join(err, fmt.Errorf("delete draft %s: %w", id, cerr))
		}
		return err
	}
	u := fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", g.uploads(), repo, r.ID, url.QueryEscape(AssetName))
	uctx, cancel := context.WithTimeout(ctx, g.transferTimeout())
	defer cancel()
	resp, err := g.do(uctx, "POST", u, bytes.NewReader(asset), "application/gzip", "")
	if err != nil {
		return nil, fail(err)
	}
	resp.Body.Close()
	var pub Release
	err = g.json(ctx, "PATCH", fmt.Sprintf("%s/repos/%s/releases/%d", g.api(), repo, r.ID), map[string]any{
		"draft": false, "make_latest": "true",
	}, &pub)
	if err != nil {
		// GitHub may have applied the PATCH and only the response was lost:
		// check before deleting anything.
		var cur Release
		gerr := g.json(context.WithoutCancel(ctx), "GET", fmt.Sprintf("%s/repos/%s/releases/%d", g.api(), repo, r.ID), nil, &cur)
		switch {
		case gerr != nil:
			return nil, fmt.Errorf("%w: release %s (id %d) left in place: publish: %w; checking it: %v; %s", ErrPublishUncertain, id, r.ID, err, gerr, uncertainHint(id))
		case cur.Complete():
			return &cur, nil
		case cur.Draft:
			return nil, fail(fmt.Errorf("publish draft release %s: %w", id, err))
		default:
			return nil, fmt.Errorf("%w: release %s (id %d) is published without a complete asset; left in place: %w; %s", ErrPublishUncertain, id, r.ID, err, uncertainHint(id))
		}
	}
	return &pub, nil
}

// uncertainHint tells the operator what to do after ErrPublishUncertain.
func uncertainHint(tag string) string {
	return fmt.Sprintf("if GitHub shows %s published with %s, nothing else is needed; otherwise re-run publish (the next successful publish prunes the stale release)", tag, AssetName)
}

// Download opens the snapshot asset of a release.
func (g *GitHub) Download(ctx context.Context, r Release) (io.ReadCloser, error) {
	for _, a := range r.Assets {
		if a.Name == AssetName {
			dctx, cancel := context.WithTimeout(ctx, g.transferTimeout())
			resp, err := g.do(dctx, "GET", a.URL, nil, "", "application/octet-stream")
			if err != nil {
				cancel()
				return nil, err
			}
			return cancelBody{resp.Body, cancel}, nil
		}
	}
	return nil, fmt.Errorf("release %s has no %s asset", r.Tag, AssetName)
}

// Prune deletes complete snapshot releases (and their tags) beyond the
// newest keep, which must be at least 1. Incomplete ones (drafts, or
// releases without the asset) never count toward keep; one is deleted only
// when it is older than the newest complete snapshot and its tag is older
// than StalePartial. A newer or recent one may be a publish still in
// progress: tags are stamped when a build starts, so an overlapping run's
// draft can be older than a release another run just published.
// A publish must therefore finish within StalePartial of its build start;
// runs that may take longer should queue (a concurrency group) rather than
// overlap.
func (g *GitHub) Prune(ctx context.Context, repo string, keep int) (int, error) {
	if keep < 1 {
		return 0, fmt.Errorf("prune: keep must be at least 1, got %d", keep)
	}
	rs, err := g.snapshotReleases(ctx, repo)
	if err != nil {
		return 0, err
	}
	n, complete := 0, 0
	for _, r := range rs {
		if r.Complete() {
			complete++
			if complete <= keep {
				continue
			}
		} else if complete == 0 || !staleTag(r.Tag) {
			continue
		}
		if err := g.deleteRelease(ctx, repo, r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// staleTag reports whether a snapshot tag's timestamp (snapshot-<UTC>[-hash],
// see bundle.SnapshotID) is older than StalePartial. Unparseable tags are
// never stale.
func staleTag(tag string) bool {
	const layout = "20060102T150405Z"
	ts := strings.TrimPrefix(tag, TagPrefix)
	if len(ts) < len(layout) {
		return false
	}
	t, err := time.Parse(layout, ts[:len(layout)])
	return err == nil && time.Since(t) > StalePartial
}

// deleteRelease deletes a release and its tag. "Already gone" is not an
// error: 404 on the release (a racing prune), 404 or 422 on the tag ref (a
// draft has no tag).
func (g *GitHub) deleteRelease(ctx context.Context, repo string, r Release) error {
	err := g.json(ctx, "DELETE", fmt.Sprintf("%s/repos/%s/releases/%d", g.api(), repo, r.ID), nil, nil)
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return err
	}
	err = g.json(ctx, "DELETE", fmt.Sprintf("%s/repos/%s/git/refs/tags/%s", g.api(), repo, url.PathEscape(r.Tag)), nil, nil)
	if err != nil && !hasStatus(err, http.StatusNotFound, http.StatusUnprocessableEntity) {
		return err
	}
	return nil
}

// ErrPublishUncertain means the publish request failed and the release's
// state could not be checked afterwards: it may or may not be published, so
// it was left in place.
var ErrPublishUncertain = errors.New("publish outcome uncertain")

// ErrNoReleases means the workspace repo has no snapshot releases yet.
var ErrNoReleases = errors.New("no snapshot releases published yet")
