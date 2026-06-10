package serve_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devenjarvis/lathe/internal/serve"
	"github.com/devenjarvis/lathe/internal/store"
)

func readExported(t *testing.T, outDir string, parts ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{outDir}, parts...)...))
	if err != nil {
		t.Fatalf("exported file missing: %v", err)
	}
	return string(data)
}

func TestExportWritesStaticSite(t *testing.T) {
	dir := t.TempDir()
	makeTestTutorial(t, dir, "test-series", true)
	makeTestTutorial(t, dir, "legacy-single", false)
	outDir := filepath.Join(t.TempDir(), "site")

	n, err := serve.NewServer(dir).Export(outDir)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if n != 2 {
		t.Errorf("Export = %d tutorials, want 2", n)
	}

	// Every part exports as .html — a plain web server would serve .md as text.
	for _, p := range []string{
		filepath.Join("test-series", "part-01.html"),
		filepath.Join("test-series", "part-02.html"),
		filepath.Join("legacy-single", "index.html"),
	} {
		if _, err := os.Stat(filepath.Join(outDir, p)); err != nil {
			t.Errorf("exported page missing: %v", err)
		}
	}

	// The whitelisted assets land flat under _static/ (fonts, bundles, favicon).
	for _, a := range []string{"fraunces.woff2", "mermaid.min.js", "katex.min.css", "favicon.svg"} {
		if _, err := os.Stat(filepath.Join(outDir, "_static", a)); err != nil {
			t.Errorf("exported asset missing: %v", err)
		}
	}
}

func TestExportListPage(t *testing.T) {
	dir := t.TempDir()
	makeTestTutorial(t, dir, "test-series", true)
	makeTestTutorial(t, dir, "legacy-single", false)
	outDir := t.TempDir()

	if _, err := serve.NewServer(dir).Export(outDir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	body := readExported(t, outDir, "index.html")

	// Cards link straight to the first part, relatively — no server redirect hop.
	if !strings.Contains(body, `class="tutorial-link" href="test-series/part-01.html"`) {
		t.Error("list card should link to the series' first exported part")
	}
	if !strings.Contains(body, `class="tutorial-link" href="legacy-single/index.html"`) {
		t.Error("list card should link to the legacy tutorial's index.html")
	}
	// Server-backed UI is gone. (Match markup, not the inlined CSS rules.)
	if strings.Contains(body, `class="delete-form"`) || strings.Contains(body, "/-/delete/") {
		t.Error("static list page must not render the delete form")
	}
	// The inlined CSS resolves fonts relative to the site root.
	if !strings.Contains(body, "url('_static/fraunces.woff2')") {
		t.Error("static list page CSS should reference fonts at _static/")
	}
	if strings.Contains(body, "url('/_static/") {
		t.Error("static list page CSS still references absolute /_static/ URLs")
	}
	if !strings.Contains(body, `href="_static/favicon.svg"`) {
		t.Error("static list page favicon should resolve relatively")
	}
	// Client-side search/filter/sort stays — it never needed the server.
	if !strings.Contains(body, `id="searchInput"`) {
		t.Error("static list page should keep the client-side search box")
	}
}

func TestExportPartPage(t *testing.T) {
	dir := t.TempDir()
	makeTestTutorial(t, dir, "test-series", true)
	outDir := t.TempDir()

	if _, err := serve.NewServer(dir).Export(outDir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	body := readExported(t, outDir, "test-series", "part-01.html")

	// Links are relative: sibling parts, the list page, and the assets.
	if !strings.Contains(body, `href="part-02.html"`) {
		t.Error("part page should link to its sibling part relatively")
	}
	if !strings.Contains(body, `href="../index.html" class="back-link"`) {
		t.Error("part page should link back to the list page relatively")
	}
	if !strings.Contains(body, `data-assets="../_static/"`) {
		t.Error("part page should carry the relative asset prefix on <body>")
	}
	if !strings.Contains(body, "url('../_static/fraunces.woff2')") {
		t.Error("part page CSS should reference fonts at ../_static/")
	}
	// Nothing on the page may call back into the local server.
	if strings.Contains(body, "/-/") {
		t.Error("static part page still references a /-/ server endpoint")
	}
	for _, marker := range []string{"data-progress-save", `id="askDrawer"`, `id="verifyForm"`, `id="extendForm"`, `id="floatingActions"`} {
		if strings.Contains(body, marker) {
			t.Errorf("static part page must not render server-backed UI (%s)", marker)
		}
	}
}

func TestExportSeriesRedirectStub(t *testing.T) {
	dir := t.TempDir()
	makeTestTutorial(t, dir, "test-series", true)
	outDir := t.TempDir()

	if _, err := serve.NewServer(dir).Export(outDir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	stub := readExported(t, outDir, "test-series", "index.html")
	if !strings.Contains(stub, `content="0;url=part-01.html"`) {
		t.Error("series index.html should meta-refresh to the first part")
	}
}

func TestExportDropsSavedProgress(t *testing.T) {
	dir := t.TempDir()
	tutDir := makeTestTutorial(t, dir, "test-series", true)
	if err := store.WriteProgress(tutDir, &store.Progress{
		Part: "part-02.md", Ratio: 0.5, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()

	if _, err := serve.NewServer(dir).Export(outDir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	// No backend to save to ⇒ no saved position anywhere: no card progress bar
	// on the list page, no saved marker data on the part page.
	if body := readExported(t, outDir, "index.html"); strings.Contains(body, `class="tutorial-progress"`) {
		t.Error("static list page must not render saved reading progress")
	}
	if body := readExported(t, outDir, "test-series", "part-02.html"); !strings.Contains(body, `data-saved-progress=""`) {
		t.Error("static part page must not carry a saved progress ratio")
	}
}

func TestExportIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	makeTestTutorial(t, dir, "test-series", true)
	outDir := t.TempDir()

	srv := serve.NewServer(dir)
	if _, err := srv.Export(outDir); err != nil {
		t.Fatalf("first Export: %v", err)
	}
	if _, err := srv.Export(outDir); err != nil {
		t.Fatalf("second Export over existing files: %v", err)
	}
}
