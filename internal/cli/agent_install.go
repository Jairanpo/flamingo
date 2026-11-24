package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Jairanpo/flamingo/internal/config"
)

// agentInstallCmd sets a computer up in one go, from the file the app
// offers: the program goes somewhere it can stay, pairs with a code from
// the app, and starts now and at every login.
func agentInstallCmd(path *string) *cobra.Command {
	var server, name, code string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set this computer up for downloads: keep the program, pair it, start it at login",
		Long: `Copies this program to a place it can stay, pairs the computer with a
one-time code from the app (Downloads → Set up this computer), and starts
the agent now and at every login:

  Linux    ~/.local/bin/flamingo, a systemd user service
  macOS    ~/Library/Application Support/Flamingo, a LaunchAgent
  Windows  %LOCALAPPDATA%\Flamingo, a Startup entry

Run it again to update the program; a paired computer needs no new code.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			cfg, err := config.LoadAgent(*path)
			if err != nil {
				return err
			}
			if server != "" {
				cfg.ServerURL = strings.TrimRight(server, "/")
			}
			if name != "" {
				cfg.DeviceName = name
			}
			if cfg.ServerURL == "" {
				return errors.New("--server is required the first time: the address you open Flamingo at")
			}

			bin, err := keepProgram()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "program: %s\n", bin)

			if code != "" {
				device, err := pairWith(*path, cfg, map[string]string{
					"code": code, "device": cfg.DeviceName, "os": runtime.GOOS})
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "paired %q with %s\n", device, cfg.ServerURL)
			} else if _, err := os.Stat(cfg.TokenFile); err != nil {
				return errors.New("not paired yet: add --code from the app (Downloads → Set up this computer)")
			} else if err := config.SaveAgent(*path, cfg); err != nil {
				return err
			}

			if err := startAtLogin(bin, *path); err != nil {
				return fmt.Errorf("starting at login: %w", err)
			}
			if _, err := exec.LookPath(cfg.Ytdlp.Path); err != nil {
				fmt.Fprintf(out, "note: %s is not installed; downloads need it (and ffmpeg):\n"+
					"  https://github.com/yt-dlp/yt-dlp#installation\n", cfg.Ytdlp.Path)
			}
			if waitForAgent(cfg.Listen, 10*time.Second) {
				fmt.Fprintf(out, "agent running. Open %s and reload: downloads are on.\n", cfg.ServerURL)
				return nil
			}
			return fmt.Errorf("the agent did not answer on %s yet; try: %s agent run", cfg.Listen, bin)
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "the address you open Flamingo at, e.g. http://localhost:8430")
	cmd.Flags().StringVar(&code, "code", "", "the one-time pairing code from the app")
	cmd.Flags().StringVar(&name, "name", "", "this computer's name (default: its host name)")
	return cmd
}

// programDir is where the program stays on each system.
func programDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Flamingo"), nil
		}
		return filepath.Join(home, "AppData", "Local", "Flamingo"), nil
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Flamingo"), nil
	default:
		return filepath.Join(home, ".local", "bin"), nil
	}
}

// keepProgram copies the running program to programDir, unless it already
// runs from there. The copy goes in beside it and is renamed over the old
// one, so a running agent keeps its file until it restarts.
func keepProgram() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if s, err := filepath.EvalSymlinks(self); err == nil {
		self = s
	}
	dir, err := programDir()
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, "flamingo")
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	if t, err := filepath.EvalSymlinks(target); err == nil && t == self {
		return target, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(dir, ".flamingo-new-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		// Windows will not replace a running program, but will rename it.
		old := target + ".old"
		_ = os.Remove(old)
		_ = os.Rename(target, old)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		// A file from the browser is quarantined; this copy is ours now.
		_ = exec.Command("xattr", "-d", "com.apple.quarantine", target).Run()
	}
	return target, nil
}

// startAtLogin registers the agent with the system's own way of starting
// programs at login, and starts (or restarts) it now.
func startAtLogin(bin, cfgPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	args := []string{"agent", "--agent-config", cfgPath, "run"}
	switch runtime.GOOS {
	case "linux":
		unit := fmt.Sprintf(`# This computer's Flamingo helper, started at login and kept running.
# Written by: flamingo agent install
[Unit]
Description=Flamingo agent (downloads for the Flamingo page)
After=network-online.target

[Service]
ExecStart=%q %s
Restart=on-failure
RestartSec=5
# yt-dlp and node often live in ~/.local/bin; a user unit's PATH is short.
Environment=PATH=%%h/.local/bin:/usr/local/bin:/usr/bin:/bin

[Install]
WantedBy=default.target
`, bin, strings.Join(quoteAll(args), " "))
		dir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "flamingo-agent.service"), []byte(unit), 0o644); err != nil {
			return err
		}
		for _, a := range [][]string{{"daemon-reload"}, {"enable", "flamingo-agent.service"}, {"restart", "flamingo-agent.service"}} {
			if b, err := exec.Command("systemctl", append([]string{"--user"}, a...)...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl --user %s: %v %s", strings.Join(a, " "), err, b)
			}
		}
		return nil

	case "darwin":
		var progArgs strings.Builder
		for _, a := range append([]string{bin}, args...) {
			fmt.Fprintf(&progArgs, "\n    <string>%s</string>", xmlEscape(a))
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.flamingo.agent</string>
  <key>ProgramArguments</key>
  <array>%s
  </array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
`, progArgs.String())
		dir := filepath.Join(home, "Library", "LaunchAgents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		p := filepath.Join(dir, "com.flamingo.agent.plist")
		if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
			return err
		}
		_ = exec.Command("launchctl", "unload", p).Run()
		if b, err := exec.Command("launchctl", "load", "-w", p).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl load: %v %s", err, b)
		}
		return nil

	case "windows":
		// A tiny script in the Startup folder runs the agent with no window.
		startup := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
		if err := os.MkdirAll(startup, 0o755); err != nil {
			return err
		}
		line := `"""` + bin + `"""`
		for _, a := range args {
			line += ` ""` + a + `""`
		}
		vbs := "' Flamingo agent: downloads for the Flamingo page. Written by: flamingo agent install\r\n" +
			`CreateObject("WScript.Shell").Run "` + line + `", 0, False` + "\r\n"
		p := filepath.Join(startup, "Flamingo agent.vbs")
		if err := os.WriteFile(p, []byte(vbs), 0o644); err != nil {
			return err
		}
		// Stop an agent already running, then start the new one.
		_ = exec.Command("taskkill", "/F", "/IM", "flamingo.exe", "/FI", "PID ne "+fmt.Sprint(os.Getpid())).Run()
		return exec.Command("wscript", p).Start()
	}
	return fmt.Errorf("starting at login is not set up for %s; run: %s agent run", runtime.GOOS, bin)
}

func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = fmt.Sprintf("%q", a)
	}
	return out
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// waitForAgent waits until the agent answers its hello.
func waitForAgent(listen string, d time.Duration) bool {
	client := &http.Client{Timeout: time.Second}
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if r, err := client.Get("http://" + listen + "/api/hello"); err == nil {
			r.Body.Close()
			if r.StatusCode == http.StatusOK {
				return true
			}
		}
	}
	return false
}
