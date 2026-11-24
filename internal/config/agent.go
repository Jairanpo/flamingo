package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Agent is the config of the program that runs on each computer. Where
// downloads go is not here: the server holds it (platforms and devices), so
// it can be changed from the app.
type Agent struct {
	Listen     string `mapstructure:"listen"`
	ServerURL  string `mapstructure:"server_url"`
	DeviceName string `mapstructure:"device_name"`
	// PageOrigins are the addresses the Flamingo page is opened at, when
	// they differ from server_url (a cloud server behind its own name, say).
	// Only pages from the server's own address, or these, may use the agent.
	PageOrigins []string `mapstructure:"page_origins"`
	// TokenFile holds the key pairing gave this computer. Kept apart from
	// the YAML so the YAML can be shared or shown without leaking it.
	TokenFile string        `mapstructure:"token_file"`
	Download  AgentDownload `mapstructure:"download"`
	Ytdlp     Ytdlp         `mapstructure:"ytdlp"`
}

type AgentDownload struct {
	// MaxHeight is the quality Download gets when none is picked.
	MaxHeight int    `mapstructure:"max_height"`
	SubLangs  string `mapstructure:"sub_langs"`
	Workers   int    `mapstructure:"workers"`
}

func configDir() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "flamingo")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "flamingo")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "flamingo")
}

// AgentPath is where the agent's YAML lives unless --agent-config says.
func AgentPath() string { return filepath.Join(configDir(), "agent.yaml") }

func agentDefaults(v *viper.Viper) {
	host, _ := os.Hostname()
	v.SetDefault("listen", "127.0.0.1:8421")
	v.SetDefault("server_url", "")
	v.SetDefault("page_origins", []string{})
	v.SetDefault("device_name", SafeDeviceName(host))
	v.SetDefault("token_file", filepath.Join(configDir(), "agent.token"))
	v.SetDefault("download.max_height", 1080)
	v.SetDefault("download.sub_langs", "en,es")
	v.SetDefault("download.workers", 2)
	v.SetDefault("ytdlp.path", "yt-dlp")
	v.SetDefault("ytdlp.extra_args", []string{})
}

// SafeDeviceName turns a host name into something a device may be called.
func SafeDeviceName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		out = "computer"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// LoadAgent reads the agent config; a missing file means defaults.
func LoadAgent(path string) (*Agent, error) {
	v := viper.New()
	agentDefaults(v)
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("FLAMINGO_AGENT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !(errors.As(err, &notFound) || errors.Is(err, os.ErrNotExist)) {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	}
	var c Agent
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for _, s := range []*string{&c.Listen, &c.ServerURL, &c.TokenFile, &c.Ytdlp.Path} {
		*s = os.ExpandEnv(*s)
	}
	c.ServerURL = strings.TrimRight(c.ServerURL, "/")
	if c.Download.Workers < 1 {
		c.Download.Workers = 1
	}
	return &c, nil
}

// SaveAgent writes the settings pairing chose. Only the keys pairing knows
// are written; anything else falls back to its default on the next load.
func SaveAgent(path string, c *Agent) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf(`# Flamingo agent: this computer's helper. Download folders are set in the
# app (Downloads), not here.
listen: %q
server_url: %q
device_name: %q
download:
  max_height: %d
  sub_langs: %q
`, c.Listen, c.ServerURL, c.DeviceName, c.Download.MaxHeight, c.Download.SubLangs)
	if len(c.PageOrigins) > 0 {
		body += "page_origins:\n"
		for _, o := range c.PageOrigins {
			body += fmt.Sprintf("  - %q\n", o)
		}
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

// ExpandHome turns a folder from the server ("~/Videos/Flamingo", or with
// backslashes on Windows) into a real path on this computer.
func ExpandHome(dir string) (string, error) {
	dir = os.ExpandEnv(strings.TrimSpace(dir))
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, filepath.FromSlash(strings.ReplaceAll(dir[1:], `\`, "/")))
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("download folder %q is not a full path", dir)
	}
	return filepath.Clean(dir), nil
}
