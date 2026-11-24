// Package config loads the server's YAML configuration through viper.
//
// Order of precedence, lowest first: built-in defaults, the YAML file, then
// environment variables named FLAMINGO_<SECTION>_<KEY> (for example
// FLAMINGO_DATABASE_URL). String values may also contain ${VAR} references, which
// are expanded from the environment, so secrets never need to be written
// into the file itself.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Server struct {
	Listen    string   `mapstructure:"listen"`
	PublicURL string   `mapstructure:"public_url"`
	Database  Database `mapstructure:"database"`
	Storage   Storage  `mapstructure:"storage"`
	Auth      Auth     `mapstructure:"auth"`
	Ytdlp     Ytdlp    `mapstructure:"ytdlp"`
}

type Database struct {
	URL            string `mapstructure:"url"`
	MigrateOnStart bool   `mapstructure:"migrate_on_start"`
}

type Storage struct {
	ThumbsDir string `mapstructure:"thumbs_dir"`
	// AgentsDir holds the agent built for each system, offered in the app
	// to set up downloads on a computer. Missing: the app says so.
	AgentsDir string `mapstructure:"agents_dir"`
}

type Auth struct {
	SessionTTL    time.Duration `mapstructure:"session_ttl"`
	SecretKeyFile string        `mapstructure:"secret_key_file"`
	Signup        string        `mapstructure:"signup"`
}

type Ytdlp struct {
	Path      string   `mapstructure:"path"`
	ExtraArgs []string `mapstructure:"extra_args"`
	// StreamMaxHeight is the quality a stream starts at; the player can
	// switch to any other on offer.
	StreamMaxHeight int `mapstructure:"stream_max_height"`
}

// DefaultPath is where the server looks when no --config flag is given.
func DefaultPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "flamingo", "server.yaml")
}

func defaults(v *viper.Viper) {
	v.SetDefault("listen", ":8420")
	v.SetDefault("public_url", "")
	v.SetDefault("database.url", "")
	v.SetDefault("database.migrate_on_start", true)
	v.SetDefault("storage.thumbs_dir", "./data/thumbs")
	v.SetDefault("storage.agents_dir", "/usr/local/share/flamingo/agents")
	v.SetDefault("auth.session_ttl", "720h")
	v.SetDefault("auth.secret_key_file", "")
	v.SetDefault("auth.signup", "invite")
	v.SetDefault("ytdlp.path", "yt-dlp")
	v.SetDefault("ytdlp.extra_args", []string{})
	v.SetDefault("ytdlp.stream_max_height", 1080)
}

// LoadServer reads the server config. A missing file is only an error when
// the path was asked for explicitly; otherwise defaults and environment
// variables are enough to run.
func LoadServer(path string, explicit bool) (*Server, error) {
	v := viper.New()
	defaults(v)
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("FLAMINGO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if explicit || !(errors.As(err, &notFound) || errors.Is(err, os.ErrNotExist)) {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	}

	var c Server
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	c.expandEnv()
	return &c, c.validate()
}

func (c *Server) expandEnv() {
	for _, s := range []*string{&c.Listen, &c.PublicURL, &c.Database.URL,
		&c.Storage.ThumbsDir, &c.Auth.SecretKeyFile, &c.Ytdlp.Path} {
		*s = os.ExpandEnv(*s)
	}
}

func (c *Server) validate() error {
	if c.Database.URL == "" {
		return errors.New("database.url is not set (in the YAML file or FLAMINGO_DATABASE_URL)")
	}
	switch c.Auth.Signup {
	case "invite", "closed":
	default:
		return fmt.Errorf("auth.signup must be invite or closed, not %q", c.Auth.Signup)
	}
	if c.Ytdlp.StreamMaxHeight < 0 {
		return errors.New("ytdlp.stream_max_height must not be negative")
	}
	if c.Auth.SessionTTL <= 0 {
		return errors.New("auth.session_ttl must be positive")
	}
	return nil
}
