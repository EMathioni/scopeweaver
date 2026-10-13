package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type oauthFixture struct {
	server    *httptest.Server
	key       *rsa.PrivateKey
	client    *Client
	store     *Store
	nonce     string
	subject   string
	audience  string
	scopes    string
	mode      string
	refreshes atomic.Int32
	grants    atomic.Int32
	revoked   atomic.Bool
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c, err := NewClient(s, "test")
	if err != nil {
		t.Fatal(err)
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oauthFixture{key: k, client: c, store: s, subject: "subject-one", audience: "oaiapp_test", scopes: "openid profile email offline_access resource.invoke " + planScope}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/discovery":
			json.NewEncoder(w).Encode(discoveryDocument{Issuer: f.server.URL, JWKS: f.server.URL + "/jwks", Revocation: f.server.URL + "/revoke"})
		case "/jwks":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kid": "test", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}})
		case "/token":
			r.ParseForm()
			if r.Form.Get("client_id") != "oaiapp_test" || r.Form.Get("resource") != c.resource {
				t.Error("incorrect OAuth client/resource")
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				f.refreshes.Add(1)
				if r.Form.Get("refresh_token") != "refresh-one" || r.Form.Has("scope") {
					t.Error("incorrect refresh grant")
				}
				if f.mode == "revoked" {
					w.WriteHeader(400)
					w.Write([]byte(`{"error":"invalid_grant","error_description":"secret-refresh-one"}`))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": "access-two", "refresh_token": "refresh-two", "expires_in": 3600, "scope": f.scopes, "token_type": "Bearer"})
				return
			}
			f.grants.Add(1)
			if r.Form.Get("code") != "test-code" || r.Form.Get("code_verifier") == "" || r.Form.Get("redirect_uri") != "http://127.0.0.1:1455/auth/callback" {
				t.Error("incorrect authorization grant")
			}
			jwtKey := k
			if f.mode == "bad_signature" {
				jwtKey, _ = rsa.GenerateKey(rand.Reader, 2048)
			}
			claims := identityClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: f.server.URL, Subject: f.subject, Audience: jwt.ClaimStrings{f.audience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, Email: "test@example.invalid", Nonce: f.nonce}
			if f.mode == "expired" {
				claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
			}
			if f.mode == "bad_issuer" {
				claims.Issuer = "https://wrong.invalid"
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "test"
			signed, _ := token.SignedString(jwtKey)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access-one", "refresh_token": "refresh-one", "id_token": signed, "expires_in": 3600, "scope": f.scopes, "token_type": "Bearer"})
		case "/revoke":
			r.ParseForm()
			if r.Form.Get("token") != "refresh-one" || r.Form.Get("client_id") != "oaiapp_test" || r.Form.Get("token_type_hint") != "refresh_token" {
				t.Error("incorrect revocation")
			}
			if f.mode == "revoke_failed" {
				w.WriteHeader(400)
				return
			}
			f.revoked.Store(true)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	c.issuer, c.discovery, c.tokenEndpoint = f.server.URL, f.server.URL+"/discovery", f.server.URL+"/token"
	return f
}

func (f *oauthFixture) begin(t *testing.T) (*LoginAttempt, string) {
	t.Helper()
	a, target, err := f.client.BeginLogin("http://127.0.0.1:1455/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	f.nonce = a.nonce
	return a, target
}

func (f *oauthFixture) callback(a *LoginAttempt) string {
	return a.redirect + "?" + url.Values{"state": {a.state}, "client_id": {"oaiapp_test"}, "code": {"test-code"}}.Encode()
}

func (f *oauthFixture) signedIn(t *testing.T) {
	t.Helper()
	a, _ := f.begin(t)
	if err := a.Finish(context.Background(), f.callback(a)); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthRegistrationAndReauthorization(t *testing.T) {
	f := newOAuthFixture(t)
	a, target := f.begin(t)
	u, _ := url.Parse(target)
	q := u.Query()
	h := sha256.Sum256([]byte(a.verifier))
	if q.Get("client_id") != dynamicClient || q.Get("agent_name_hint") != "ScopeWeaver" || q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(h[:]) || q.Get("code_challenge_method") != "S256" || q.Get("resource") != resource {
		t.Fatal("incorrect public-client authorization")
	}
	if err := a.Finish(context.Background(), f.callback(a)); err != nil {
		t.Fatal(err)
	}
	if a.Finish(context.Background(), f.callback(a)) == nil || f.grants.Load() != 1 {
		t.Fatal("code replay accepted")
	}
	c, err := f.store.Load("test")
	if err != nil || c.ClientID != "oaiapp_test" || c.Subject != f.subject || c.RefreshToken != "refresh-one" || c.HostID != a.hostID {
		t.Fatal("incorrect stored registration", err)
	}
	second, target := f.begin(t)
	u, _ = url.Parse(target)
	q = u.Query()
	if q.Get("client_id") != c.ClientID || q.Get("agent_name_hint") != "" || q.Get("ext_agent_host_id") != a.hostID || q.Get("id_token_hint") != c.IDToken || second.state == a.state || second.verifier == a.verifier || second.nonce == a.nonce {
		t.Fatal("incorrect returning sign-in")
	}
	// Returning callbacks are permitted to omit client_id.
	if err := second.Finish(context.Background(), second.redirect+"?"+url.Values{"state": {second.state}, "code": {"test-code"}}.Encode()); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthRejectsUntrustedCallbacks(t *testing.T) {
	f := newOAuthFixture(t)
	for _, variant := range []string{"state", "client", "host", "duplicate", "missing_code", "missing_client"} {
		t.Run(variant, func(t *testing.T) {
			a, _ := f.begin(t)
			u, _ := url.Parse(f.callback(a))
			q := u.Query()
			switch variant {
			case "state":
				q.Set("state", "wrong")
			case "client":
				q.Set("client_id", dynamicClient)
			case "host":
				u.Host = "localhost:1455"
			case "duplicate":
				q.Add("code", "another")
			case "missing_code":
				q.Del("code")
			case "missing_client":
				q.Del("client_id")
			}
			u.RawQuery = q.Encode()
			if a.Finish(context.Background(), u.String()) == nil {
				t.Fatal("untrusted callback accepted")
			}
		})
	}
	if f.grants.Load() != 0 {
		t.Fatal("untrusted callback reached token exchange")
	}
	a, _ := f.begin(t)
	if err := a.Finish(context.Background(), a.redirect+"?"+url.Values{"state": {a.state}, "error": {"access_denied"}}.Encode()); err == nil || !a.Completed() || f.grants.Load() != 0 {
		t.Fatal("consent denial mishandled")
	}
}

func TestOAuthValidatesIdentityAndPlanPermission(t *testing.T) {
	for _, mode := range []string{"bad_nonce", "bad_audience", "bad_signature", "bad_issuer", "expired", "no_plan_scope", "wrong_returning_identity", "wrong_returning_client"} {
		t.Run(mode, func(t *testing.T) {
			f := newOAuthFixture(t)
			if strings.HasPrefix(mode, "wrong_returning") {
				f.signedIn(t)
			}
			a, _ := f.begin(t)
			f.mode = mode
			switch mode {
			case "bad_nonce":
				f.nonce = "wrong"
			case "bad_audience":
				f.audience = "wrong"
			case "no_plan_scope":
				f.scopes = "openid offline_access resource.invoke"
			case "wrong_returning_identity":
				f.subject = "other-subject"
			}
			callback := f.callback(a)
			if mode == "wrong_returning_client" {
				callback = strings.Replace(callback, "oaiapp_test", "oaiapp_other", 1)
			}
			if err := a.Finish(context.Background(), callback); err == nil {
				t.Fatal("invalid authentication accepted")
			}
			_, _, connected := f.client.Account()
			if strings.HasPrefix(mode, "wrong_returning") {
				if !connected || f.client.credentials.Subject != "subject-one" {
					t.Fatal("invalid login replaced active account")
				}
			} else if connected {
				t.Fatal("invalid login saved")
			}
		})
	}
}

func TestConcurrentRefreshRotationAndPersistence(t *testing.T) {
	f := newOAuthFixture(t)
	f.signedIn(t)
	f.client.credentials.ExpiresAt = time.Now().Add(-time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := f.client.Token(context.Background())
			if err != nil || token != "access-two" {
				t.Error("refresh failed", err)
			}
		}()
	}
	wg.Wait()
	if f.refreshes.Load() != 1 {
		t.Fatal("refresh token rotated more than once")
	}
	c, err := f.store.Load("test")
	if err != nil || c.RefreshToken != "refresh-two" || c.AccessToken != "access-two" {
		t.Fatal("replacement not persisted")
	}
	token, err := f.client.RefreshAfterUnauthorized(context.Background(), "access-one")
	if err != nil || token != "access-two" || f.refreshes.Load() != 1 {
		t.Fatal("stale 401 rotated new token")
	}
}

func TestInvalidGrantClearsSessionWithoutLeakingSecrets(t *testing.T) {
	f := newOAuthFixture(t)
	f.signedIn(t)
	f.mode = "revoked"
	f.client.credentials.ExpiresAt = time.Now()
	_, err := f.client.Token(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid grant leaked response")
	}
	if _, err = f.client.Token(context.Background()); err == nil || f.refreshes.Load() != 1 {
		t.Fatal("expired refresh token reused")
	}
	c, err := f.store.Load("test")
	if err != nil || c.AccessToken != "" || c.RefreshToken != "" || c.ClientID != "oaiapp_test" {
		t.Fatal("invalid session not cleared")
	}
}

func TestEarliestRefreshIsHonored(t *testing.T) {
	f := newOAuthFixture(t)
	f.signedIn(t)
	f.client.credentials.ExpiresAt = time.Now().Add(30 * time.Second)
	f.client.credentials.EarliestRefreshAt = time.Now().Add(20 * time.Second)
	if token, err := f.client.Token(context.Background()); err != nil || token != "access-one" || f.refreshes.Load() != 0 {
		t.Fatal("refreshed before allowed time")
	}
	if _, err := f.client.RefreshAfterUnauthorized(context.Background(), "access-one"); err == nil || f.refreshes.Load() != 0 {
		t.Fatal("401 bypassed refresh timing")
	}
}

func TestLogoutRevokesAndRetainsRegistration(t *testing.T) {
	for _, failed := range []bool{false, true} {
		f := newOAuthFixture(t)
		f.signedIn(t)
		if failed {
			f.mode = "revoke_failed"
		}
		err := f.client.Logout(context.Background())
		if (err != nil) != failed || f.revoked.Load() == failed {
			t.Fatal("incorrect revocation result", err)
		}
		c, e := f.store.Load("test")
		if e != nil || c.AccessToken != "" || c.RefreshToken != "" || c.IDToken != "" || c.ClientID != "oaiapp_test" || c.HostID == "" {
			t.Fatal("logout erased registration or retained tokens")
		}
	}
}

func TestPrivateStoreLockAndAccounts(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if second, e := OpenStore(dir); e == nil {
		second.Close()
		t.Fatal("two writers accepted")
	}
	if _, e := s.Load("../escape"); e == nil {
		t.Fatal("account path escape accepted")
	}
	host, err := s.HostID()
	if err != nil {
		t.Fatal(err)
	}
	if next, _ := s.HostID(); next != host {
		t.Fatal("host ID changed")
	}
	key, err := s.BridgeKey()
	if err != nil || len(key) < 32 {
		t.Fatal("bridge key missing")
	}
	if next, _ := s.BridgeKey(); next != key {
		t.Fatal("bridge key changed")
	}
	s.Save("one", Credentials{Subject: "one"})
	s.Save("two", Credentials{Subject: "two"})
	if c, _ := s.Load("one"); c.Subject != "one" {
		t.Fatal("accounts mixed")
	}
	if runtime.GOOS != "windows" {
		for _, file := range []string{"host.json", "bridge-key.json", "one.json", "two.json"} {
			info, _ := os.Stat(filepath.Join(dir, file))
			if info.Mode().Perm() != 0600 {
				t.Fatal("unsafe permissions")
			}
		}
		os.Chmod(filepath.Join(dir, "one.json"), 0644)
		if _, err := s.Load("one"); err == nil {
			t.Fatal("world-readable credentials accepted")
		}
	}
}
