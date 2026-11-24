package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Jairanpo/flamingo/internal/agent"
	"github.com/Jairanpo/flamingo/internal/config"
)

// agentCmd is the helper each computer runs. It has its own config file;
// it never touches the database.
func agentCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "This computer's helper: downloads, local playback, and the page at 127.0.0.1:8421",
	}
	cmd.PersistentFlags().StringVar(&path, "agent-config", config.AgentPath(), "agent config file")
	cmd.AddCommand(agentPairCmd(&path), agentRunCmd(&path))
	return cmd
}

func agentRunCmd(path *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Serve the page and keep this computer's downloads",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadAgent(*path)
			if err != nil {
				return err
			}
			if cfg.ServerURL == "" {
				return errors.New("not paired yet: run flamingo agent pair --server http://<server>:<port>")
			}
			b, err := os.ReadFile(cfg.TokenFile)
			if err != nil {
				return fmt.Errorf("no device token (%w): run flamingo agent pair", err)
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			a, err := agent.New(cfg, strings.TrimSpace(string(b)), log)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return a.Run(ctx)
		},
	}
}

func agentPairCmd(path *string) *cobra.Command {
	var server, name, user, code string
	var pwStdin bool
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Sign this computer in to the server (once)",
		Long: `Signs this computer in with your Flamingo account and keeps the key the
server hands back. Pairing the same name again replaces the key, so a
reinstalled computer keeps its list of downloads.

With --code, a one-time code from the app (Downloads) stands in for your
user name and password.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadAgent(*path)
			if err != nil {
				return err
			}
			if server != "" {
				cfg.ServerURL = strings.TrimRight(server, "/")
			}
			if cfg.ServerURL == "" {
				return errors.New("--server is required the first time (http://<server>:<port>)")
			}
			if name != "" {
				cfg.DeviceName = name
			}
			req := map[string]string{"device": cfg.DeviceName, "os": runtime.GOOS}
			if code != "" {
				req["code"] = code
			} else {
				in := bufio.NewReader(cmd.InOrStdin())
				if user == "" {
					fmt.Fprint(cmd.ErrOrStderr(), "Flamingo user: ")
					line, _ := in.ReadString('\n')
					user = strings.TrimSpace(line)
				}
				pw, err := askOnce(cmd, in, pwStdin)
				if err != nil {
					return err
				}
				req["user"], req["password"] = user, pw
			}
			body, _ := json.Marshal(req)
			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Post(cfg.ServerURL+"/api/agent/pair", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("reaching %s: %w", cfg.ServerURL, err)
			}
			defer resp.Body.Close()
			var out struct{ Device, Token, Error string }
			_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
			if resp.StatusCode != http.StatusOK || out.Token == "" {
				if out.Error == "" {
					out.Error = resp.Status
				}
				return errors.New("pairing refused: " + out.Error)
			}
			if err := os.MkdirAll(filepath.Dir(cfg.TokenFile), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(cfg.TokenFile, []byte(out.Token+"\n"), 0o600); err != nil {
				return err
			}
			if err := config.SaveAgent(*path, cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "paired %q with %s\nnext: flamingo agent run, then open http://%s\n",
				out.Device, cfg.ServerURL, cfg.Listen)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "the server's address, e.g. http://192.168.1.192:8430")
	cmd.Flags().StringVar(&name, "name", "", "this computer's name (default: its host name)")
	cmd.Flags().StringVar(&user, "user", "", "your Flamingo user name")
	cmd.Flags().StringVar(&code, "code", "", "a one-time pairing code from the app, instead of a password")
	cmd.Flags().BoolVar(&pwStdin, "password-stdin", false, "read the password from standard input")
	return cmd
}

// askOnce reads a password: without echo on a terminal, or one line of
// standard input for scripts.
func askOnce(cmd *cobra.Command, in *bufio.Reader, fromStdin bool) (string, error) {
	f, ok := cmd.InOrStdin().(*os.File)
	if fromStdin || !ok || !term.IsTerminal(int(f.Fd())) {
		line, err := in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(cmd.ErrOrStderr())
	return string(b), err
}
