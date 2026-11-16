package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Jairanpo/flamingo/internal/db"
	"github.com/Jairanpo/flamingo/internal/library"
	"github.com/Jairanpo/flamingo/internal/secret"
)

// userTogglCmd connects (or disconnects) a person's Toggl Track account.
// The token is sealed with auth.secret_key_file before it is stored.
func userTogglCmd(load loader) *cobra.Command {
	var fromStdin, clear bool
	var workspace int64
	cmd := &cobra.Command{
		Use:   "toggl NAME",
		Short: "Connect a user's Toggl Track account (asks for their API token)",
		Long: `Stores a user's Toggl Track API token, sealed with the server's secret key,
so the player's timer button works for them. The token is on
https://track.toggl.com/profile under "API Token".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := load(cmd)
			if err != nil {
				return err
			}
			box, err := secret.Load(cfg.Auth.SecretKeyFile)
			if err != nil {
				return err
			}
			if box == nil && !clear {
				return secret.ErrNoKey
			}
			token := ""
			if !clear {
				if token, err = readToken(cmd, fromStdin); err != nil {
					return err
				}
				if token == "" {
					return errors.New("no token given (use --clear to disconnect)")
				}
			}
			pool, err := db.Open(cmd.Context(), cfg.Database.URL)
			if err != nil {
				return err
			}
			defer pool.Close()
			if err := library.SetTogglAccount(cmd.Context(), pool, box, args[0], token, workspace); err != nil {
				return err
			}
			if clear {
				fmt.Fprintf(cmd.OutOrStdout(), "disconnected Toggl for %s\n", args[0])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "connected Toggl for %s\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "token-stdin", false, "read the token from standard input")
	cmd.Flags().BoolVar(&clear, "clear", false, "disconnect: forget the stored token")
	cmd.Flags().Int64Var(&workspace, "workspace", 0, "workspace id to file entries under (default: the account's default)")
	return cmd
}

func readToken(cmd *cobra.Command, fromStdin bool) (string, error) {
	in := cmd.InOrStdin()
	if fromStdin {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errors.New("no terminal to ask for the token; use --token-stdin")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Toggl API token: ")
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(cmd.ErrOrStderr())
	return strings.TrimSpace(string(b)), err
}
