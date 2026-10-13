package chatgpt

import (
	"encoding/json"
	"errors"
	"github.com/Autumn-27/artex/locale"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Credentials are never returned by the bridge's HTTP interface.
type Credentials struct {
	Issuer            string    `json:"issuer"`
	Subject           string    `json:"subject"`
	Email             string    `json:"email,omitempty"`
	ClientID          string    `json:"client_id"`
	HostID            string    `json:"ext_agent_host_id"`
	IDToken           string    `json:"id_token,omitempty"`
	AccessToken       string    `json:"access_token,omitempty"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	Scopes            []string  `json:"scopes"`
	ExpiresAt         time.Time `json:"expires_at"`
	EarliestRefreshAt time.Time `json:"earliest_refresh_at,omitempty"`
}

// Store holds a process-wide exclusive lock on one credential directory. Keep
// login/logout and serve in separate invocations; never race rotating tokens.
type Store struct {
	dir  string
	lock *os.File
	mu   sync.Mutex
}

var accountName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func DefaultDirectory() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "scopeweaver", "chatgpt"), nil
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, locale.NewError("credential directory must be a real directory")
	}
	if err := secureDirectory(dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, locale.NewError("ChatGPT credentials are in use; stop the other bridge/login process first")
	}
	return &Store{dir: dir, lock: f}, nil
}

func (s *Store) Close() error { return s.lock.Close() }

func (s *Store) accountPath(account string) (string, error) {
	if !accountName.MatchString(account) {
		return "", locale.NewError("account label must contain 1-64 letters, digits, underscores or hyphens")
	}
	return filepath.Join(s.dir, account+".json"), nil
}

func (s *Store) Load(account string) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.accountPath(account)
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	err = readPrivateJSON(p, &c)
	return c, err
}

func (s *Store) Save(account string, c Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.accountPath(account)
	if err != nil {
		return err
	}
	return writePrivateJSON(p, c)
}

func readPrivateJSON(path string, v any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return locale.NewError("credential file must be a regular file")
	}
	if err := checkPrivatePermissions(info); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return locale.NewError("invalid credential file")
	}
	return nil
}

func writePrivateJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// HostID is stable across accounts, restarts, sign-outs and reauthorization.
func (s *Store) HostID() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := filepath.Join(s.dir, "host.json")
	var h struct {
		ID string `json:"ext_agent_host_id"`
	}
	err := readPrivateJSON(p, &h)
	if err == nil {
		if _, e := uuid.Parse(stringsTrimUUID(h.ID)); e != nil {
			return "", locale.NewError("invalid host ID")
		}
		return h.ID, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	h.ID = "urn:uuid:" + uuid.NewString()
	return h.ID, writePrivateJSON(p, h)
}

func stringsTrimUUID(id string) string {
	const prefix = "urn:uuid:"
	if len(id) > len(prefix) && id[:len(prefix)] == prefix {
		return id[len(prefix):]
	}
	return ""
}

// BridgeKey is a separate local credential, never the OpenAI access token.
func (s *Store) BridgeKey() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := filepath.Join(s.dir, "bridge-key.json")
	var k struct {
		Key string `json:"key"`
	}
	err := readPrivateJSON(p, &k)
	if err == nil {
		if len(k.Key) < 32 {
			return "", locale.NewError("invalid local bridge key")
		}
		return k.Key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	k.Key, err = randomString(32)
	if err != nil {
		return "", err
	}
	return k.Key, writePrivateJSON(p, k)
}
