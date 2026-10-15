// Package export renders the UI's print view to PDF or SVG with headless
// Chrome. Chrome is optional: without it, export fails with an install hint
// and every other command keeps working.
package export

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/shermanhuman/cartographer/internal/query"
	"github.com/shermanhuman/cartographer/internal/server"
)

// ErrNoChrome is returned when no Chrome or Chromium executable is found.
var ErrNoChrome = errors.New("export needs Google Chrome or Chromium: install it (macOS: `brew install --cask google-chrome`; Debian/Ubuntu: `apt install chromium`) or set CHROME_PATH to its executable")

// FindChrome locates a Chrome-family browser: $CHROME_PATH, then common
// install locations, then names on PATH.
func FindChrome() (string, error) {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		return "", fmt.Errorf("CHROME_PATH=%s is not an executable file; %w", p, ErrNoChrome)
	}
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		for _, app := range []string{"Google Chrome", "Chromium", "Google Chrome Canary", "Microsoft Edge", "Brave Browser"} {
			paths = append(paths, "/Applications/"+app+".app/Contents/MacOS/"+app)
		}
		if home, err := os.UserHomeDir(); err == nil {
			paths = append(paths, home+"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if dir := os.Getenv(env); dir != "" {
				paths = append(paths, dir+`\Google\Chrome\Application\chrome.exe`)
			}
		}
	default:
		paths = append(paths, "/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/snap/bin/chromium")
	}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "headless-shell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", ErrNoChrome
}

// Target selects what the export shows; exactly one field besides Depth.
type Target struct {
	Around, Context, Table, Annotation string
	Depth                              int
}

// Fragment resolves a target into the UI deep link (the part after #).
func Fragment(ctx context.Context, q *query.Q, t Target) (string, error) {
	set := 0
	for _, s := range []string{t.Around, t.Context, t.Table, t.Annotation} {
		if s != "" {
			set++
		}
	}
	if set != 1 {
		return "", errors.New("export: choose exactly one of --around, --context, --table or --annotation")
	}
	v := url.Values{}
	switch {
	case t.Around != "":
		e, err := q.Resolve(ctx, t.Around)
		if err != nil {
			return "", err
		}
		v.Set("view", "entity")
		v.Set("entity", e.ID)
		v.Set("focus", e.ID)
		if e.Kind == "function" {
			v.Set("tab", "code")
		}
		if t.Depth > 1 {
			v.Set("depth", fmt.Sprint(t.Depth))
		}
	case t.Context != "":
		e, err := q.ResolveKind(ctx, t.Context, "context")
		if err != nil {
			return "", err
		}
		if e.Kind != "context" {
			var c string
			q.S.DB.QueryRowContext(ctx, `SELECT context FROM modules WHERE snapshot_id = ? AND id = ?`, q.Snapshot, e.ID).Scan(&c)
			if c == "" {
				return "", fmt.Errorf("%s is a %s, not a context", e.ID, e.Kind)
			}
			e = &query.Entity{ID: c}
		}
		v.Set("view", "entity")
		v.Set("entity", e.ID)
		v.Set("focus", e.ID)
	case t.Table != "":
		e, err := q.ResolveKind(ctx, t.Table, "table")
		if err != nil {
			return "", err
		}
		if e.Kind != "table" {
			return "", fmt.Errorf("%s is a %s, not a table", e.ID, e.Kind)
		}
		v.Set("view", "entity")
		v.Set("entity", e.ID)
		v.Set("focus", e.ID)
		v.Set("tab", "data")
	default:
		id := strings.TrimPrefix(t.Annotation, "ann:")
		var kind string
		if err := q.S.DB.QueryRowContext(ctx, `SELECT kind FROM annotations WHERE snapshot_id = ? AND id = ?`, q.Snapshot, id).Scan(&kind); err != nil {
			return "", fmt.Errorf("no annotation %q in snapshot %s", t.Annotation, q.Snapshot)
		}
		switch kind {
		case "journey":
			v.Set("view", id)
		case "investigation":
			v.Set("view", "home")
			v.Set("investigation", id)
		default:
			v.Set("view", "entity")
			v.Set("entity", "ann:"+id)
			v.Set("focus", "ann:"+id)
		}
	}
	return v.Encode(), nil
}

// Options control one export.
type Options struct {
	Format     string // "pdf" or "svg"
	Mode       string // "page" or "snippet"
	Snapshot   string // "" = newest
	ChromePath string // "" = FindChrome
	Timeout    time.Duration
}

// Run serves h on a random 127.0.0.1 port, opens the deep link fragment
// with &print=<mode> in headless Chrome, waits for body[data-ready="1"] and
// returns the PDF or the graph's standalone SVG.
func Run(ctx context.Context, h http.Handler, fragment string, o Options) ([]byte, error) {
	if o.Format != "pdf" && o.Format != "svg" {
		return nil, fmt.Errorf("export: format %q (want pdf or svg)", o.Format)
	}
	if o.Mode == "" {
		o.Mode = "page"
	}
	if o.Mode != "page" && o.Mode != "snippet" {
		return nil, fmt.Errorf("export: mode %q (want page or snippet)", o.Mode)
	}
	if o.Timeout == 0 {
		o.Timeout = 90 * time.Second
	}
	chrome := o.ChromePath
	if chrome == "" {
		var err error
		if chrome, err = FindChrome(); err != nil {
			return nil, err
		}
	}
	ln, err := server.Listen("127.0.0.1", 0)
	if err != nil {
		return nil, err
	}
	sctx, stop := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- server.Serve(sctx, ln, h) }()
	defer func() {
		stop()
		<-served
	}()

	link := fmt.Sprintf("http://127.0.0.1:%d/#%s&print=%s", ln.Addr().(*net.TCPAddr).Port, fragment, o.Mode)
	if o.Snapshot != "" {
		link += "&snapshot=" + url.QueryEscape(o.Snapshot)
	}

	opts := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]),
		chromedp.ExecPath(chrome),
		chromedp.WindowSize(1440, 1000),
		chromedp.Flag("hide-scrollbars", true),
	)
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	actx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	cctx, cancelCtx := chromedp.NewContext(actx, chromedp.WithErrorf(func(string, ...any) {}))
	defer cancelCtx()
	tctx, cancelT := context.WithTimeout(cctx, o.Timeout)
	defer cancelT()

	err = chromedp.Do(tctx,
		chromedp.Navigate(link),
		chromedp.WaitReady(chromedp.CSS(`body[data-ready="1"]`)),
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("export: the page did not finish rendering within %s (%s)", o.Timeout, link)
		}
		return nil, fmt.Errorf("export: chrome: %w", err)
	}
	failure, err := chromedp.Run(tctx, chromedp.Evaluate[string](`document.body.dataset.error || ""`))
	if err != nil {
		return nil, fmt.Errorf("export: chrome: %w", err)
	}
	if failure != "" {
		return nil, fmt.Errorf("export: the map could not render this view: %s", failure)
	}
	footer, err := chromedp.Run(tctx, chromedp.Evaluate[string](`document.getElementById("export-footer")?.textContent || ""`))
	if err != nil {
		return nil, fmt.Errorf("export: chrome: %w", err)
	}
	if !strings.Contains(footer, "map ") {
		return nil, errors.New("export: the print view has no provenance footer")
	}
	var out []byte
	switch o.Format {
	case "pdf":
		out, err = chromedp.Run(tctx, chromedp.PrintToPDF(chromedp.PDFPrintBackground(), chromedp.PDFPreferCSSPageSize()))
	case "svg":
		var svg string
		svg, err = chromedp.Run(tctx, chromedp.Evaluate[string](`window.cartographerSVG ? window.cartographerSVG() : ""`))
		if err == nil && svg == "" {
			err = errors.New("this view has no graph to export as SVG (journeys and browsers are page-only; use --pdf)")
		}
		out = []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + svg + "\n")
	}
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	return out, nil
}
