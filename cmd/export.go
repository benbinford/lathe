package cmd

import (
	"fmt"

	"github.com/devenjarvis/lathe/internal/config"
	"github.com/devenjarvis/lathe/internal/serve"
	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export [dir]",
	Short: "Export all tutorials as a static HTML site",
	Long: `Export every stored tutorial as a self-contained static HTML site that any
plain web server (nginx, GitHub Pages, python -m http.server, …) can host.

The exported pages keep the lathe serve reading experience — the list page
with client-side search/filter/sort, dark mode, mermaid diagrams, LaTeX math —
but drop everything that needs the local server: delete, the verify/extend/ask
handoff buttons, and saved reading progress. All links are relative, so the
site works from any subpath.

dir defaults to ./lathe-site. Existing files are overwritten (export is
idempotent); files for tutorials deleted since a previous export are not
removed, so export into a fresh or dedicated directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		outDir := "lathe-site"
		if len(args) == 1 {
			outDir = args[0]
		}
		dir, err := config.TutorialsDir()
		if err != nil {
			return err
		}
		n, err := serve.NewServer(dir).Export(outDir)
		if err != nil {
			return err
		}
		noun := "tutorials"
		if n == 1 {
			noun = "tutorial"
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Exported %d %s to %s\n", n, noun, outDir)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(exportCmd)
}
