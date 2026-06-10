package serve

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devenjarvis/lathe/internal/store"
	"github.com/devenjarvis/lathe/internal/voice"
)

//go:embed layout.html list.html components.html
var templateFS embed.FS

// styles.css is the entire design system (tokens + components). It's loaded
// once at startup and injected inline via the {{define "head"}} partial,
// mirroring how HighlightCSS is injected — no extra request, no FOUC.
//
//go:embed styles.css
var stylesCSS string

//go:embed static/mermaid.min.js
//go:embed static/katex.min.js static/katex-auto-render.min.js static/katex.min.css
//go:embed static/favicon.svg
//go:embed static/fonts/fraunces.woff2 static/fonts/newsreader.woff2 static/fonts/newsreader-italic.woff2 static/fonts/jetbrains-mono.woff2
//go:embed static/fonts/KaTeX_*.woff2
var staticFS embed.FS

type Server struct {
	tutorialsDir string
	layoutTmpl   *template.Template
	listTmpl     *template.Template
	highlightCSS template.CSS
	designCSS    template.CSS
}

func NewServer(tutorialsDir string) *Server {
	funcMap := template.FuncMap{
		"add":          func(a, b int) int { return a + b },
		"cardProgress": cardProgress,
	}
	// components.html is parsed into both template sets so its shared partials
	// ({{define "head"}}, "badge", "themeToggle") are available to each page.
	layoutTmpl := template.Must(template.New("layout.html").Funcs(funcMap).ParseFS(templateFS, "components.html", "layout.html"))
	listTmpl := template.Must(template.New("list.html").Funcs(funcMap).ParseFS(templateFS, "components.html", "list.html"))
	css, err := HighlightCSS()
	if err != nil {
		panic(fmt.Sprintf("lathe: failed to build syntax-highlight CSS: %v", err))
	}
	return &Server{
		tutorialsDir: tutorialsDir,
		layoutTmpl:   layoutTmpl,
		listTmpl:     listTmpl,
		highlightCSS: css,
		designCSS:    template.CSS(stylesCSS),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_static/{name}", s.handleStatic)
	mux.HandleFunc("GET /{$}", s.handleList)
	mux.HandleFunc("GET /{slug}/", s.handleTutorial)
	mux.HandleFunc("GET /{slug}/{part}", s.handlePart)
	mux.HandleFunc("POST /-/delete/{slug}", s.handleDelete)
	mux.HandleFunc("POST /-/ask/{slug}/{part}", s.handleAsk)
	mux.HandleFunc("POST /-/progress/{slug}/{part}", s.handleProgress)
	mux.HandleFunc("POST /-/extend/{slug}", s.handleExtend)
	mux.HandleFunc("POST /-/verify/{slug}", s.handleVerify)
	return mux
}

// staticAssets whitelists the embedded files we expose under /_static/. Keeping
// it explicit means no embed.FS path can be coaxed out of the binary by an
// unexpected route — even though the {name} wildcard already can't contain a
// slash, this is the cheap belt-and-suspenders check.
var staticAssets = map[string]string{
	"mermaid.min.js":           "application/javascript; charset=utf-8",
	"katex.min.js":             "application/javascript; charset=utf-8",
	"katex-auto-render.min.js": "application/javascript; charset=utf-8",
	"katex.min.css":            "text/css; charset=utf-8",
	"favicon.svg":              "image/svg+xml",
	"fraunces.woff2":           "font/woff2",
	"newsreader.woff2":         "font/woff2",
	"newsreader-italic.woff2":  "font/woff2",
	"jetbrains-mono.woff2":     "font/woff2",
}

// The KaTeX math fonts join the whitelist from the embed FS itself rather than
// by hand-listing all 20: the //go:embed glob (static/fonts/KaTeX_*.woff2) is
// the explicit boundary, and reading it back keeps the whitelist in lockstep
// across KaTeX upgrades. The vendored katex.min.css references them flat
// (url(KaTeX_…)) so they resolve under the same /_static/<name>.woff2 route as
// the text fonts.
func init() {
	entries, err := staticFS.ReadDir("static/fonts")
	if err != nil {
		panic(fmt.Sprintf("lathe: embedded font dir unreadable: %v", err))
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "KaTeX_") && strings.HasSuffix(name, ".woff2") {
			staticAssets[name] = "font/woff2"
		}
	}
}

// staticAssetBytes resolves a whitelisted /_static/<name> asset to its embedded
// bytes. Fonts are exposed at flat names (single-segment route, whitelisted
// above) but live under static/fonts/ on disk. Shared by the HTTP handler and
// the static exporter so the whitelist stays the single boundary.
func staticAssetBytes(name string) ([]byte, error) {
	if _, ok := staticAssets[name]; !ok {
		return nil, fs.ErrNotExist
	}
	embedPath := "static/" + name
	if strings.HasSuffix(name, ".woff2") {
		embedPath = "static/fonts/" + name
	}
	return staticFS.ReadFile(embedPath)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	contentType, ok := staticAssets[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := staticAssetBytes(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

// pageContext carries the per-mode rendering knobs that differ between the
// local server (absolute URLs, server-backed UI) and the static exporter
// (relative URLs, server-backed UI suppressed). Templates receive them as the
// Static / AssetPrefix / ListHref data keys plus per-part hrefs.
type pageContext struct {
	static      bool
	assetPrefix string              // prefix for the /_static assets, e.g. "/_static/" or "../_static/"
	listHref    string              // href back to the list page, from a part page
	partHref    func(string) string // href of a part file, from a sibling part page
	css         template.CSS        // design CSS with its font URLs resolved for this page
}

// servePageContext is the local-server mode: absolute paths rooted at /.
func (s *Server) servePageContext(slug string) pageContext {
	return pageContext{
		assetPrefix: "/_static/",
		listHref:    "/",
		partHref:    func(part string) string { return "/" + slug + "/" + part },
		css:         s.designCSS,
	}
}

// loadTutorials reads every tutorial under dir, newest first. Unreadable
// entries are skipped, matching the long-standing list-page behavior. The
// flat, newest-first ordering is the JS-off initial order; the client sort
// control re-orders from here.
func loadTutorials(dir string) ([]*store.Tutorial, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var tutorials []*store.Tutorial
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tut, err := store.ReadMetadata(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		tutorials = append(tutorials, tut)
	}
	sort.SliceStable(tutorials, func(a, b int) bool {
		return tutorials[a].Created.After(tutorials[b].Created)
	})
	return tutorials, nil
}

// renderListPage renders the list page for either mode. hrefs maps each
// tutorial slug to its card link (mode-dependent: "/slug/" served, relative
// "slug/part-01.html" exported).
func (s *Server) renderListPage(tutorials []*store.Tutorial, hrefs map[string]string, pc pageContext) ([]byte, error) {
	var buf bytes.Buffer
	if err := s.listTmpl.Execute(&buf, map[string]any{
		"Tutorials":    tutorials,
		"Hrefs":        hrefs,
		"Static":       pc.static,
		"AssetPrefix":  pc.assetPrefix,
		"CSS":          pc.css,
		"HighlightCSS": s.highlightCSS,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	tutorials, err := loadTutorials(s.tutorialsDir)
	if err != nil {
		http.Error(w, "could not read tutorials", http.StatusInternalServerError)
		return
	}
	hrefs := make(map[string]string, len(tutorials))
	for _, tut := range tutorials {
		hrefs[tut.Slug] = "/" + tut.Slug + "/"
	}
	html, err := s.renderListPage(tutorials, hrefs, s.servePageContext(""))
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(html)
}

func (s *Server) safeTutorialPath(parts ...string) (string, bool) {
	p := filepath.Join(append([]string{s.tutorialsDir}, parts...)...)
	if !strings.HasPrefix(p, s.tutorialsDir+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

func (s *Server) handleTutorial(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	tutDir, ok := s.safeTutorialPath(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	tut, err := store.ReadMetadata(tutDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Any tutorial with parts (single or series) lives in part-NN.md files, not
	// index.md. Prefer a valid saved-progress part when one is saved, otherwise keep
	// the historical first-part redirect. The index.md fallback is only for
	// legacy tutorials that were never split into parts.
	if len(tut.Parts) > 0 {
		part := tut.Parts[0]
		if tut.Progress != nil && isKnownPart(tut, tut.Progress.Part) {
			part = tut.Progress.Part
		}
		http.Redirect(w, r, fmt.Sprintf("/%s/%s", slug, part), http.StatusFound)
		return
	}
	s.renderPart(w, tut, tutDir, "index.md")
}

func (s *Server) handlePart(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	part := r.PathValue("part")
	tutDir, ok := s.safeTutorialPath(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	tut, err := store.ReadMetadata(tutDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Only render files the metadata declares as parts (plus the legacy
	// index.md fallback). Without this, the {part} route would happily read and
	// render any file in the tutorial dir — metadata.json, verify-result.json.
	if !isKnownPart(tut, part) {
		http.NotFound(w, r)
		return
	}
	s.renderPart(w, tut, tutDir, part)
}

// isKnownPart reports whether part is one of the tutorial's declared parts or
// the legacy single-file index.md.
func isKnownPart(tut *store.Tutorial, part string) bool {
	// index.md is the legacy single-file fallback — valid only when the
	// tutorial was never split into parts (matching handleTutorial).
	if part == "index.md" {
		return len(tut.Parts) == 0
	}
	for _, p := range tut.Parts {
		if p == part {
			return true
		}
	}
	return false
}

// sameOrigin reports whether a state-changing request originated from a page
// served by this server. It rejects a *present* Origin or Referer that points
// elsewhere — the defense against CSRF, where another site (or a LAN device)
// POSTs to our predictable localhost port. A request with neither header (e.g.
// curl, or a same-origin form POST that omits Origin) is allowed.
func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return isLocalOrigin(origin)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		return isLocalOrigin(ref)
	}
	return true
}

// isLocalOrigin reports whether a URL's host is loopback. We match on host
// rather than an exact port because the listen port is configurable (--port).
func isLocalOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	slug := r.PathValue("slug")
	tutDir, ok := s.safeTutorialPath(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(tutDir)
	if err != nil || !info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if err := os.RemoveAll(tutDir); err != nil {
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// SeriesEntry is a row in the "In this series" list rendered at the bottom of
// each series part. Title is precomputed from the part filename so the template
// doesn't need to call into the store package; Href is the mode-dependent link
// to the part (absolute when served, relative sibling when exported).
type SeriesEntry struct {
	Slug    string
	Href    string
	Title   string
	Number  int
	Current bool
}

func (s *Server) renderPart(w http.ResponseWriter, tut *store.Tutorial, tutDir, part string) {
	html, err := s.renderPartPage(tut, tutDir, part, s.servePageContext(tut.Slug))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "part not found", http.StatusNotFound)
			return
		}
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(html)
}

// renderPartPage renders one tutorial part to a full HTML page for either mode.
// A missing part file surfaces as fs.ErrNotExist so the HTTP caller can map it
// to a 404.
func (s *Server) renderPartPage(tut *store.Tutorial, tutDir, part string, pc pageContext) ([]byte, error) {
	src, err := os.ReadFile(filepath.Join(tutDir, part))
	if err != nil {
		return nil, err
	}
	content, toc, err := RenderMarkdownWithTOC(src)
	if err != nil {
		return nil, err
	}

	var prevPart, nextPart, prevTitle, nextTitle, prevHref, nextHref string
	var prevNumber, nextNumber, currentNumber int
	var seriesTOC []SeriesEntry
	isLast := true
	if tut.IsSeries() {
		seriesTOC = make([]SeriesEntry, 0, len(tut.Parts))
		for i, p := range tut.Parts {
			seriesTOC = append(seriesTOC, SeriesEntry{
				Slug:    p,
				Href:    pc.partHref(p),
				Title:   store.SlugToTitle(strings.TrimSuffix(p, ".md")),
				Number:  i + 1,
				Current: p == part,
			})
			if p == part {
				currentNumber = i + 1
				isLast = i == len(tut.Parts)-1
				if i > 0 {
					prevPart = tut.Parts[i-1]
					prevTitle = store.SlugToTitle(strings.TrimSuffix(prevPart, ".md"))
					prevHref = pc.partHref(prevPart)
					prevNumber = i
				}
				if i < len(tut.Parts)-1 {
					nextPart = tut.Parts[i+1]
					nextTitle = store.SlugToTitle(strings.TrimSuffix(nextPart, ".md"))
					nextHref = pc.partHref(nextPart)
					nextNumber = i + 2
				}
			}
		}
	}

	// Surface the verifier's recorded result. On failure it explains what broke
	// (part/step/error); on verified/skipped it carries the CheckedAt timestamp
	// we show as "Verified <date>". Best-effort: a missing or malformed
	// verify-result.json simply renders no panel and no date.
	var verifyResult *store.VerifyResult
	switch tut.Status {
	case store.StatusFailed, store.StatusVerified, store.StatusSkipped:
		if vr, err := store.ReadVerifyResult(tutDir); err == nil {
			verifyResult = vr
		}
	}

	// On verified/skipped, format the verifier's timestamp as a friendly date for
	// the "Verified <date>" provenance line. Best-effort: an unparseable or empty
	// CheckedAt simply yields no date.
	var verifiedDate string
	if verifyResult != nil && (tut.Status == store.StatusVerified || tut.Status == store.StatusSkipped) {
		if ts, err := time.Parse(time.RFC3339, verifyResult.CheckedAt); err == nil {
			verifiedDate = ts.Format("Jan 2, 2006")
		}
	}

	// Count inline [!UNVERIFIED] callouts so the page can flag, near the badge,
	// how many claims the author couldn't ground in a source. Derived at render
	// time from the rendered HTML, so it stays live as parts change with no
	// metadata bookkeeping.
	unverifiedCount := bytes.Count(content, []byte("callout-unverified"))

	// Render the voice spec body (markdown) for the byline's inline reveal.
	// Best-effort and only for voiced tutorials: a deleted/unresolvable custom
	// voice — or a spec that fails to render — yields empty HTML, so the byline
	// still shows the name but renders no <details>. Pre-feature tutorials (empty
	// Voice) get no reveal — the byline shows the model only, matching the old
	// footer behavior.
	var voiceSpec template.HTML
	if tut.Voice != "" {
		if v, err := voice.Resolve(tut.Voice); err == nil {
			if rendered, rerr := RenderMarkdown([]byte(v.Body())); rerr == nil {
				voiceSpec = template.HTML(rendered)
			}
		}
	}

	currentProgress := currentPartProgress(tut, part)

	var buf bytes.Buffer
	if err := s.layoutTmpl.Execute(&buf, map[string]any{
		"Title":             tut.Title,
		"Tutorial":          tut,
		"VerifyResult":      verifyResult,
		"VerifiedDate":      verifiedDate,
		"UnverifiedCount":   unverifiedCount,
		"VoiceSpec":         voiceSpec,
		"CurrentPart":       part,
		"CurrentProgress":   currentProgress,
		"CurrentPartNumber": currentNumber,
		"Content":           template.HTML(content),
		"CSS":               pc.css,
		"HighlightCSS":      s.highlightCSS,
		"Static":            pc.static,
		"AssetPrefix":       pc.assetPrefix,
		"ListHref":          pc.listHref,
		"PrevPart":          prevPart,
		"NextPart":          nextPart,
		"PrevTitle":         prevTitle,
		"NextTitle":         nextTitle,
		"PrevHref":          prevHref,
		"NextHref":          nextHref,
		"PrevNumber":        prevNumber,
		"NextNumber":        nextNumber,
		"TOC":               toc,
		"SeriesTOC":         seriesTOC,
		"IsLastPart":        isLast,
		"NextPartNumber":    len(tut.Parts) + 1,
		"PendingPartNumber": pendingPartNumber(tut.PendingPart, len(tut.Parts)+1),
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func pendingPartNumber(pendingPart string, fallback int) int {
	if pendingPart == "" {
		return fallback
	}
	s := strings.TrimSuffix(strings.TrimPrefix(pendingPart, "part-"), ".md")
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func currentPartProgress(tut *store.Tutorial, part string) *store.Progress {
	if tut.Progress == nil || tut.Progress.Part != part {
		return nil
	}
	return tut.Progress
}
