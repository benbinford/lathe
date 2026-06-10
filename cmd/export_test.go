package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExportCmdWritesSite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "index.md"), []byte("# Hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := storeCmd.RunE(storeCmd, []string{src}); err != nil {
		t.Fatalf("store: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "site")
	var out bytes.Buffer
	exportCmd.SetOut(&out)
	t.Cleanup(func() { exportCmd.SetOut(nil) })
	if err := exportCmd.RunE(exportCmd, []string{outDir}); err != nil {
		t.Fatalf("export: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "index.html")); err != nil {
		t.Errorf("export did not write the list page: %v", err)
	}
	slug := filepath.Base(src)
	if _, err := os.Stat(filepath.Join(outDir, slug, "index.html")); err != nil {
		t.Errorf("export did not write the tutorial page: %v", err)
	}
	if got := out.String(); got != "Exported 1 tutorial to "+outDir+"\n" {
		t.Errorf("output = %q", got)
	}
}
