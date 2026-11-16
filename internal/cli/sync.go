package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Jairanpo/yt/internal/catalog"
	"github.com/Jairanpo/yt/internal/db"
	"github.com/Jairanpo/yt/internal/ytdlp"
)

func syncCmd(load loader) *cobra.Command {
	var opts catalog.SyncOptions
	var limit int
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "List every tracked channel and playlist again",
		Long: `Asks YouTube for the current contents of everything anyone tracks
and updates the shared catalog: new uploads, removed videos, ones gone
private. Sources everyone has shelved are skipped unless --shelved is given;
--source syncs just one, shelved or not.

--limit N lists only the newest N videos of each source, which is quick but
cannot notice removals.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			pool, err := db.Open(ctx, cfg.Database.URL)
			if err != nil {
				return err
			}
			defer pool.Close()
			cat := &catalog.Catalog{Pool: pool, YT: ytdlp.Runner{Path: cfg.Ytdlp.Path, ExtraArgs: cfg.Ytdlp.ExtraArgs}}

			targets, skipped, err := cat.Targets(ctx, opts)
			if err != nil {
				return err
			}
			if len(targets) == 0 && skipped == 0 {
				return fmt.Errorf("nothing is tracked yet — add something from the page first")
			}
			out := cmd.OutOrStdout()
			newTotal, failed := 0, 0
			for _, t := range targets {
				res, err := cat.Sync(ctx, t, limit)
				if err != nil {
					failed++
					fmt.Fprintf(out, "✗ %-9s %-36.36s %v\n", t.Kind, t.Name, err)
					continue
				}
				flag := "no change"
				if res.New > 0 {
					flag = fmt.Sprintf("+%d new", res.New)
				}
				fmt.Fprintf(out, "✓ %-9s %-36.36s %5d catalogued  %s\n", t.Kind, t.Name, res.Total, flag)
				newTotal += res.New
			}
			fmt.Fprintf(out, "\n%d new video(s) across %d source(s).\n", newTotal, len(targets))
			if skipped > 0 {
				fmt.Fprintf(out, "%d shelved source(s) skipped — yt sync --shelved to include them\n", skipped)
			}
			if failed > 0 {
				return fmt.Errorf("%d source(s) failed", failed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Source, "source", "", "sync only this source id (UC…, PL…, PICKS:UC…)")
	cmd.Flags().BoolVar(&opts.Shelved, "shelved", false, "include sources everyone has shelved")
	cmd.Flags().IntVar(&limit, "limit", 0, "list only the newest N videos of each source")
	return cmd
}
