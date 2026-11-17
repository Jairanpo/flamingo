// Package cli wires the cobra commands for the flamingo binary.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Jairanpo/flamingo/internal/config"
	"github.com/Jairanpo/flamingo/internal/db"
	"github.com/Jairanpo/flamingo/internal/secret"
	"github.com/Jairanpo/flamingo/internal/server"
)

var version = "dev" // set with -ldflags "-X github.com/Jairanpo/flamingo/internal/cli.version=..."

func Execute() error {
	return newRoot().Execute()
}

func newRoot() *cobra.Command {
	var cfgPath string
	root := &cobra.Command{
		Use:           "flamingo",
		Short:         "A shared, catalog-first library for the YouTube you learn from",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&cfgPath, "config", "c", "",
		"server config file (default "+config.DefaultPath()+")")

	load := func(cmd *cobra.Command) (*config.Server, error) {
		explicit := cmd.Flags().Changed("config")
		path := cfgPath
		if !explicit {
			path = config.DefaultPath()
		}
		return config.LoadServer(path, explicit)
	}

	root.AddCommand(serveCmd(load), migrateCmd(load), importCmd(load), syncCmd(load), userCmd(load), agentCmd(), versionCmd())
	return root
}

type loader func(*cobra.Command) (*config.Server, error)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), version) },
	}
}

func serveCmd(load loader) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the central server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load(cmd)
			if err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			pool, err := db.Open(ctx, cfg.Database.URL)
			if err != nil {
				return err
			}
			defer pool.Close()
			mig, err := db.NewMigrator(pool)
			if err != nil {
				return err
			}
			defer mig.Close()

			if cfg.Database.MigrateOnStart {
				res, err := mig.Up(ctx)
				if err != nil {
					return fmt.Errorf("migrating: %w", err)
				}
				for _, r := range res {
					log.Info("migrated", "version", r.Source.Version, "file", r.Source.Path)
				}
			}
			box, err := secret.Load(cfg.Auth.SecretKeyFile)
			if err != nil {
				return err
			}
			if box == nil {
				log.Warn("auth.secret_key_file is not set: the Toggl timer stays off")
			}
			return server.New(cfg, pool, mig, log).WithSecrets(box).Run(ctx)
		},
	}
}

func migrateCmd(load loader) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage the database schema",
	}
	with := func(fn func(context.Context, *cobra.Command, *db.Migrator) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
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
			mig, err := db.NewMigrator(pool)
			if err != nil {
				return err
			}
			defer mig.Close()
			return fn(ctx, cmd, mig)
		}
	}
	out := func(cmd *cobra.Command, format string, a ...any) {
		fmt.Fprintf(cmd.OutOrStdout(), format+"\n", a...)
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply every pending migration",
			RunE: with(func(ctx context.Context, cmd *cobra.Command, m *db.Migrator) error {
				res, err := m.Up(ctx)
				if err != nil {
					return err
				}
				if len(res) == 0 {
					out(cmd, "already up to date")
				}
				for _, r := range res {
					out(cmd, "applied %d  %s  (%s)", r.Source.Version, r.Source.Path, r.Duration)
				}
				return nil
			}),
		},
		&cobra.Command{
			Use:   "down",
			Short: "Roll back the most recent migration",
			RunE: with(func(ctx context.Context, cmd *cobra.Command, m *db.Migrator) error {
				r, err := m.Down(ctx)
				if err != nil {
					return err
				}
				out(cmd, "rolled back %d  %s", r.Source.Version, r.Source.Path)
				return nil
			}),
		},
		&cobra.Command{
			Use:   "status",
			Short: "List migrations and whether each is applied",
			RunE: with(func(ctx context.Context, cmd *cobra.Command, m *db.Migrator) error {
				st, err := m.Status(ctx)
				if err != nil {
					return err
				}
				for _, s := range st {
					when := "pending"
					if !s.AppliedAt.IsZero() {
						when = s.AppliedAt.Local().Format("2006-01-02 15:04")
					}
					out(cmd, "%5d  %-16s  %s", s.Source.Version, when, s.Source.Path)
				}
				return nil
			}),
		},
	)
	return cmd
}
