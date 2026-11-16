package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Jairanpo/yt/internal/db"
	"github.com/Jairanpo/yt/internal/importer"
	"github.com/Jairanpo/yt/internal/ytdlp"
)

func importCmd(load loader) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Bring data in from elsewhere",
	}

	var user, display, device string
	var offline bool
	sqlite := &cobra.Command{
		Use:   "sqlite [catalog.db]",
		Short: "Import the Python app's library for one user",
		Long: `Reads the Python app's SQLite library (default
~/.local/share/ytlocal/catalog.db) and writes it to Postgres in one
transaction. The shared catalog is merged; the user's own library, and the
device's list of downloads when --device is given, are replaced. Safe to
run again.

Playlists and topics whose channel the old file never recorded are looked
up with yt-dlp, about a second each; --offline skips that and leaves them
unknown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := load(cmd)
			if err != nil {
				return err
			}
			path := filepath.Join(xdgData(), "ytlocal", "catalog.db")
			if len(args) == 1 {
				path = args[0]
			}
			if _, err := os.Stat(path); err != nil {
				return err
			}

			ctx := cmd.Context()
			pool, err := db.Open(ctx, cfg.Database.URL)
			if err != nil {
				return err
			}
			defer pool.Close()

			opts := importer.Options{SQLitePath: path, UserName: user, DisplayName: display, DeviceName: device}
			if !offline {
				opts.Resolver = ytdlp.Runner{Path: cfg.Ytdlp.Path, ExtraArgs: cfg.Ytdlp.ExtraArgs}
			}
			rep, err := importer.Run(ctx, pool, opts)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "imported %s into user %s (id %d)\n\n", path, user, rep.UserID)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
			for _, c := range rep.Counts {
				fmt.Fprintf(tw, "%d\t  %s\n", c.Rows, c.Table)
			}
			tw.Flush()
			if len(rep.Warnings) > 0 {
				fmt.Fprintf(out, "\n%d notes:\n", len(rep.Warnings))
				for _, w := range rep.Warnings {
					fmt.Fprintln(out, "  -", w)
				}
			}
			return nil
		},
	}
	sqlite.Flags().StringVarP(&user, "user", "u", "", "user name to import into (created if missing; the first user becomes admin)")
	sqlite.Flags().StringVar(&display, "display-name", "", "display name for a newly created user")
	sqlite.Flags().StringVar(&device, "device", "", "record the downloaded files as being on this device")
	sqlite.Flags().BoolVar(&offline, "offline", false, "don't ask YouTube about channels the file lacks")
	_ = sqlite.MarkFlagRequired("user")

	cmd.AddCommand(sqlite)
	return cmd
}

func xdgData() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}
