package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Jairanpo/flamingo/internal/auth"
	"github.com/Jairanpo/flamingo/internal/db"
)

func userCmd(load loader) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage the people who can sign in",
	}
	with := func(fn func(ctx context.Context, cmd *cobra.Command, args []string, st *auth.Store) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			cfg, err := load(cmd)
			if err != nil {
				return err
			}
			pool, err := db.Open(cmd.Context(), cfg.Database.URL)
			if err != nil {
				return err
			}
			defer pool.Close()
			return fn(cmd.Context(), cmd, args, &auth.Store{Pool: pool, SessionTTL: cfg.Auth.SessionTTL})
		}
	}
	out := func(cmd *cobra.Command, format string, a ...any) {
		fmt.Fprintf(cmd.OutOrStdout(), format+"\n", a...)
	}

	var display string
	var admin, fromStdin bool
	add := &cobra.Command{
		Use:   "add NAME",
		Short: "Create a user (asks for their password)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(ctx context.Context, cmd *cobra.Command, args []string, st *auth.Store) error {
			pw, err := readPassword(cmd, fromStdin)
			if err != nil {
				return err
			}
			u, err := st.CreateUser(ctx, args[0], display, pw, admin)
			if err != nil {
				return err
			}
			role := "member"
			if u.IsAdmin() {
				role = "admin"
			}
			out(cmd, "created %s (%s, %s)", u.Name, u.DisplayName, role)
			return nil
		}),
	}
	add.Flags().StringVar(&display, "display-name", "", "the name shown in the app (default: the user name)")
	add.Flags().BoolVar(&admin, "admin", false, "may manage users and devices (the first user always is)")
	add.Flags().BoolVar(&fromStdin, "password-stdin", false, "read the password from standard input")

	var pwStdin bool
	passwd := &cobra.Command{
		Use:   "passwd NAME",
		Short: "Set a user's password (signs them out everywhere)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(ctx context.Context, cmd *cobra.Command, args []string, st *auth.Store) error {
			pw, err := readPassword(cmd, pwStdin)
			if err != nil {
				return err
			}
			if err := st.SetPassword(ctx, args[0], pw); err != nil {
				return err
			}
			out(cmd, "password set for %s; their old sessions are signed out", args[0])
			return nil
		}),
	}
	passwd.Flags().BoolVar(&pwStdin, "password-stdin", false, "read the password from standard input")

	list := &cobra.Command{
		Use:   "list",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: with(func(ctx context.Context, cmd *cobra.Command, _ []string, st *auth.Store) error {
			users, err := st.Users(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tDISPLAY NAME\tROLE\tPASSWORD\tSINCE")
			for _, u := range users {
				pw := "set"
				if !u.HasPassword {
					pw = "none — can't sign in"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Name, u.DisplayName, strings.ToLower(u.Role), pw,
					u.CreatedAt.Local().Format("2006-01-02"))
			}
			return tw.Flush()
		}),
	}

	var demote bool
	role := &cobra.Command{
		Use:   "admin NAME",
		Short: "Make a user an admin (--remove to undo)",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(ctx context.Context, cmd *cobra.Command, args []string, st *auth.Store) error {
			if err := st.SetRole(ctx, args[0], !demote); err != nil {
				return err
			}
			if demote {
				out(cmd, "%s is now a member", args[0])
			} else {
				out(cmd, "%s is now an admin", args[0])
			}
			return nil
		}),
	}
	role.Flags().BoolVar(&demote, "remove", false, "make them a member again")

	signout := &cobra.Command{
		Use:   "signout NAME",
		Short: "Sign a user out of every browser",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(ctx context.Context, cmd *cobra.Command, args []string, st *auth.Store) error {
			n, err := st.EndAllSessions(ctx, args[0])
			if err != nil {
				return err
			}
			out(cmd, "signed %s out of %d session(s)", args[0], n)
			return nil
		}),
	}

	var yes bool
	remove := &cobra.Command{
		Use:   "remove NAME",
		Short: "Delete a user and their whole library",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(ctx context.Context, cmd *cobra.Command, args []string, st *auth.Store) error {
			if _, err := st.UserByName(ctx, args[0]); err != nil {
				return err
			}
			if !yes {
				return errors.New("this deletes their tracked channels, progress, pins and Toggl settings; add --yes to go ahead")
			}
			if err := st.DeleteUser(ctx, args[0]); err != nil {
				return err
			}
			out(cmd, "removed %s", args[0])
			return nil
		}),
	}
	remove.Flags().BoolVar(&yes, "yes", false, "really delete")

	cmd.AddCommand(add, passwd, list, role, signout, remove, userTogglCmd(load))
	return cmd
}

// readPassword asks twice on a terminal (without echo), or reads one line
// from standard input for scripts.
func readPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	in := cmd.InOrStdin()
	if fromStdin {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errors.New("no terminal to ask for a password; use --password-stdin")
	}
	ask := func(prompt string) (string, error) {
		fmt.Fprint(cmd.ErrOrStderr(), prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return string(b), err
	}
	pw, err := ask("Password: ")
	if err != nil {
		return "", err
	}
	if len(pw) < auth.MinPasswordLen {
		return "", auth.ErrWeakPassword
	}
	again, err := ask("Again: ")
	if err != nil {
		return "", err
	}
	if pw != again {
		return "", errors.New("the two passwords differ")
	}
	return pw, nil
}
