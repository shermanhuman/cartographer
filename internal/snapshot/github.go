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
)

// TagPrefix marks releases that hold snapshots.
const TagPrefix = "snapshot-"

// AssetName is the release asset holding the packed snapshot.
const AssetName = "snapshot.tar.gz"

// GitHub is a minimal REST client for releases.
type GitHub struct {
	API     string // default https://api.github.com
	Uploads string // default https://uploads.github.com
	Token   string
	HTTP    *http.Client
}

// Token finds a GitHub token: $GH_TOKEN, $GITHUB_TOKEN, then `gh auth token`.
func Token() string {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	for _, argv := range [][]string{{"gh", "auth", "token"}, {"mise", "exec", "--", "gh", "auth", "token"}} {
		if out, err := exec.Command(argv[0], argv[1:]...).Output(); err == nil {
			if t := strings.TrimSpace(string(out)); t != "" {
				return t
			}
		}
	}
	return ""
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
	return http.DefaultClient
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
		hint := ""
		if resp.StatusCode == 401 || resp.StatusCode == 404 {
			hint = " (check access to the repository and the token: GH_TOKEN, GITHUB_TOKEN or `gh auth login`)"
		}
		return nil, fmt.Errorf("github %s %s: %s: %s%s", method, u, resp.Status, strings.TrimSpace(string(msg)), hint)
	}
	return resp, nil
}

func (g *GitHub) json(ctx context.Context, method, u string, in, out any) error {
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
	Assets []Asset `json:"assets"`
}

type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Snapshots lists snapshot releases in repo (owner/name), newest first.
func (g *GitHub) Snapshots(ctx context.Context, repo string) ([]Release, error) {
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
	// Tags embed a UTC timestamp, so lexical order is chronological.
	sort.Slice(all, func(i, j int) bool { return all[i].Tag > all[j].Tag })
	return all, nil
}

// Publish creates a release tagged with the snapshot id and uploads the asset.
func (g *GitHub) Publish(ctx context.Context, repo, id, notes string, asset []byte) (*Release, error) {
	var r Release
	err := g.json(ctx, "POST", fmt.Sprintf("%s/repos/%s/releases", g.api(), repo), map[string]any{
		"tag_name": id, "name": id, "body": notes, "draft": false, "prerelease": false, "make_latest": "true",
	}, &r)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/repos/%s/releases/%d/assets?name=%s", g.uploads(), repo, r.ID, url.QueryEscape(AssetName))
	resp, err := g.do(ctx, "POST", u, bytes.NewReader(asset), "application/gzip", "")
	if err != nil {
		// Don't leave a release without its asset behind.
		g.deleteRelease(ctx, repo, r)
		return nil, err
	}
	resp.Body.Close()
	return &r, nil
}

// Download opens the snapshot asset of a release.
func (g *GitHub) Download(ctx context.Context, r Release) (io.ReadCloser, error) {
	for _, a := range r.Assets {
		if a.Name == AssetName {
			resp, err := g.do(ctx, "GET", a.URL, nil, "", "application/octet-stream")
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		}
	}
	return nil, fmt.Errorf("release %s has no %s asset", r.Tag, AssetName)
}

// Prune deletes snapshot releases (and their tags) beyond the newest keep.
func (g *GitHub) Prune(ctx context.Context, repo string, keep int) (int, error) {
	rs, err := g.Snapshots(ctx, repo)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := keep; i < len(rs); i++ {
		if err := g.deleteRelease(ctx, repo, rs[i]); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (g *GitHub) deleteRelease(ctx context.Context, repo string, r Release) error {
	if err := g.json(ctx, "DELETE", fmt.Sprintf("%s/repos/%s/releases/%d", g.api(), repo, r.ID), nil, nil); err != nil {
		return err
	}
	err := g.json(ctx, "DELETE", fmt.Sprintf("%s/repos/%s/git/refs/tags/%s", g.api(), repo, url.PathEscape(r.Tag)), nil, nil)
	if err != nil && !strings.Contains(err.Error(), "422") && !strings.Contains(err.Error(), "404") {
		return err
	}
	return nil
}

// ErrNoReleases means the workspace repo has no snapshot releases yet.
var ErrNoReleases = errors.New("no snapshot releases published yet")
