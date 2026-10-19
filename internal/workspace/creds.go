// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

// Credentials is ~/.config/ore/credentials (0600): one token per server,
// written by `ore login`. ORE_TOKEN takes precedence over it everywhere.
type Credentials struct {
	Version int                         `json:"version"`
	Servers map[string]ServerCredential `json:"servers"`
}

// ServerCredential is one server's token and the user it resolved to.
type ServerCredential struct {
	Token string `json:"token"`
	User  string `json:"user,omitempty"`
}

// CredentialsPath is where the credentials live: $ORE_CONFIG_DIR/credentials,
// else $XDG_CONFIG_HOME/ore/credentials, else ~/.config/ore/credentials.
func CredentialsPath() string {
	if d := os.Getenv("ORE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "credentials")
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "ore", "credentials")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ore", "credentials")
}

// LoadCredentials reads the file; an absent file is an empty set.
func LoadCredentials() (*Credentials, error) {
	c := &Credentials{Version: 1, Servers: map[string]ServerCredential{}}
	p := CredentialsPath()
	if p == "" {
		return c, nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.Servers == nil {
		c.Servers = map[string]ServerCredential{}
	}
	return c, nil
}

// Save writes the file with mode 0600 in a 0700 directory.
func (c *Credentials) Save() error {
	p := CredentialsPath()
	if p == "" {
		return errors.New("no home directory for the credentials file; set ORE_CONFIG_DIR")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Set records a server's token.
func (c *Credentials) Set(server, token, user string) {
	c.Servers[NormalizeServer(server)] = ServerCredential{Token: token, User: user}
}

// Get returns a server's credential.
func (c *Credentials) Get(server string) (ServerCredential, bool) {
	sc, ok := c.Servers[NormalizeServer(server)]
	return sc, ok
}

// NormalizeServer makes URLs comparable: a scheme, no trailing slash.
func NormalizeServer(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return s
}

// TokenFor resolves the token for server: ORE_TOKEN, then the credentials
// file, then (for a server on this machine) the data directory's token.
func TokenFor(server string) (token, user string) {
	if t := os.Getenv("ORE_TOKEN"); t != "" {
		return t, ""
	}
	if c, err := LoadCredentials(); err == nil {
		if sc, ok := c.Get(server); ok && sc.Token != "" {
			return sc.Token, sc.User
		}
	}
	data := os.Getenv("ORE_DATA")
	if data == "" {
		data = "ore-data"
	}
	if b, err := os.ReadFile(filepath.Join(data, "token")); err == nil {
		return strings.TrimSpace(string(b)), ""
	}
	return "", ""
}

var userClean = regexp.MustCompile(`[^a-z0-9._-]+`)

// LocalUser is the name the hidden branch carries when the server has no
// identity extension: ORE_USER, else the operating-system user.
func LocalUser() string {
	if u := os.Getenv("ORE_USER"); u != "" {
		return CleanName(u)
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return CleanName(u.Username)
	}
	return "user"
}

// CleanName makes a name safe inside a ref.
func CleanName(s string) string {
	s = userClean.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "user"
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}
