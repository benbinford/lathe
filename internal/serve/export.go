package serve

import (
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"strings"
)

// htmlPartName maps a stored part filename (part-NN.md or the legacy index.md)
// to the page name it exports as (part-NN.html / index.html). Plain web servers
// would serve a .md file as text, so exported pages always carry .html.
func htmlPartName(part string) string {
	return strings.TrimSuffix(part, ".md") + ".html"
}

// exportCSS resolves the design system's absolute /_static/ font URLs against
// prefix so the inlined stylesheet works at any page depth in the exported
// tree. The served pages keep the absolute form (see servePageContext).
func exportCSS(prefix string) template.CSS {
	return template.CSS(strings.ReplaceAll(stylesCSS, "/_static/", prefix))
}

// exportPartContext is the static-export mode for pages one directory below
// the site root (<slug>/part-NN.html): everything resolves relatively, so the
// site works from any subpath (GitHub Pages project sites, file servers under
// a prefix) with no configuration.
func exportPartContext() pageContext {
	return pageContext{
		static:      true,
		assetPrefix: "../_static/",
		listHref:    "../index.html",
		partHref:    htmlPartName,
		css:         exportCSS("../_static/"),
	}
}

// Export writes the full static site — list page, every tutorial part, and the
// embedded assets — under outDir, creating it if needed and overwriting
// existing files (idempotent, like `lathe skills install`). It returns the
// number of tutorials exported.
//
// Static mode drops everything that needs the local server: the delete,
// verify, extend, and ask UI, and saved reading progress (there is no backend
// to persist to, so no stale position is rendered either). Layout:
//
//	outDir/index.html             the list page
//	outDir/_static/<name>         fonts, mermaid, KaTeX, favicon
//	outDir/<slug>/part-NN.html    one page per part (index.html for legacy
//	                              single-file tutorials)
//	outDir/<slug>/index.html      a redirect stub to the first part, so shared
//	                              <slug>/ URLs still land somewhere
func (s *Server) Export(outDir string) (int, error) {
	tutorials, err := loadTutorials(s.tutorialsDir)
	if err != nil {
		return 0, err
	}
	// Reading progress is server-side persistence; a static site has nowhere to
	// save to, so don't render a saved position anywhere.
	for _, tut := range tutorials {
		tut.Progress = nil
	}

	staticDir := filepath.Join(outDir, "_static")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		return 0, err
	}
	for name := range staticAssets {
		data, err := staticAssetBytes(name)
		if err != nil {
			return 0, fmt.Errorf("embedded asset %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(staticDir, name), data, 0644); err != nil {
			return 0, err
		}
	}

	// List page at the site root: cards link straight to each tutorial's first
	// part (no server, no redirect hop).
	hrefs := make(map[string]string, len(tutorials))
	for _, tut := range tutorials {
		first := "index.md"
		if len(tut.Parts) > 0 {
			first = tut.Parts[0]
		}
		hrefs[tut.Slug] = tut.Slug + "/" + htmlPartName(first)
	}
	listPage, err := s.renderListPage(tutorials, hrefs, pageContext{
		static:      true,
		assetPrefix: "_static/",
		css:         exportCSS("_static/"),
	})
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.html"), listPage, 0644); err != nil {
		return 0, err
	}

	pc := exportPartContext()
	for _, tut := range tutorials {
		tutDir := filepath.Join(s.tutorialsDir, tut.Slug)
		outTutDir := filepath.Join(outDir, tut.Slug)
		if err := os.MkdirAll(outTutDir, 0755); err != nil {
			return 0, err
		}
		parts := tut.Parts
		if len(parts) == 0 {
			parts = []string{"index.md"}
		}
		for _, part := range parts {
			page, err := s.renderPartPage(tut, tutDir, part, pc)
			if err != nil {
				return 0, fmt.Errorf("export %s/%s: %w", tut.Slug, part, err)
			}
			if err := os.WriteFile(filepath.Join(outTutDir, htmlPartName(part)), page, 0644); err != nil {
				return 0, err
			}
		}
		// Parts-based tutorials get a redirect stub at <slug>/index.html so a
		// shared <slug>/ URL still lands on the first part — the static stand-in
		// for handleTutorial's redirect. Legacy single-file tutorials already
		// exported their content as index.html above.
		if len(tut.Parts) > 0 {
			stub := redirectStub(htmlPartName(tut.Parts[0]))
			if err := os.WriteFile(filepath.Join(outTutDir, "index.html"), stub, 0644); err != nil {
				return 0, err
			}
		}
	}
	return len(tutorials), nil
}

// redirectStub is a minimal meta-refresh page pointing at target (a sibling
// file name). Part names come from metadata, so escape them anyway.
func redirectStub(target string) []byte {
	t := html.EscapeString(target)
	return []byte(fmt.Sprintf(`<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><meta http-equiv="refresh" content="0;url=%s"><link rel="canonical" href="%s"></head>
<body><p>Redirecting to <a href="%s">%s</a>…</p></body></html>
`, t, t, t, t))
}
