package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Autumn-27/artex/locale"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	issuer        = "https://auth.openai.com"
	resource      = "https://api.openai.com/v1"
	dynamicClient = "dynamic_agent_client"
	planScope     = "chatgpt.tokens.use.direct"
)

// Client exclusively owns one account registration's refresh lifecycle.
type Client struct {
	mu                                                    sync.Mutex
	store                                                 *Store
	account                                               string
	credentials                                           Credentials
	http                                                  *http.Client
	issuer, resource, authorize, tokenEndpoint, discovery string
}

func NewClient(store *Store, account string) (*Client, error) {
	if _, err := store.accountPath(account); err != nil {
		return nil, err
	}
	c, err := store.Load(account)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if c.ClientID != "" && (c.Issuer != issuer || c.ClientID == dynamicClient || c.Subject == "") {
		return nil, locale.NewError("invalid account registration")
	}
	return &Client{store: store, account: account, credentials: c,
		http:   &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect},
		issuer: issuer, resource: resource, authorize: issuer + "/api/accounts/authorize",
		tokenEndpoint: issuer + "/api/accounts/oauth/token", discovery: issuer + "/.well-known/openid-configuration"}, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type LoginAttempt struct {
	mu                                       sync.Mutex
	client                                   *Client
	state, nonce, verifier, redirect, hostID string
	previous                                 Credentials
	used                                     bool
}

var ErrAttemptUsed = locale.NewError("authorization attempt already completed")

func (a *LoginAttempt) Completed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.used
}

// BeginLogin must be called after opening the callback listener. URL includes
// an ID-token hint on reauthorization; display it to the user, never log it.
func (c *Client) BeginLogin(redirect string) (*LoginAttempt, string, error) {
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/auth/callback" || u.RawQuery != "" || u.User != nil || u.Fragment != "" {
		return nil, "", locale.NewError("OAuth callback must be http://127.0.0.1:PORT/auth/callback")
	}
	hostID, err := c.store.HostID()
	if err != nil {
		return nil, "", err
	}
	a := &LoginAttempt{client: c, redirect: redirect, hostID: hostID}
	if a.state, err = randomString(32); err != nil {
		return nil, "", err
	}
	if a.nonce, err = randomString(32); err != nil {
		return nil, "", err
	}
	if a.verifier, err = randomString(32); err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	a.previous = c.credentials
	c.mu.Unlock()
	hash := sha256.Sum256([]byte(a.verifier))
	q := url.Values{
		"client_id": {dynamicClient}, "agent_name_hint": {"ScopeWeaver"},
		"ext_agent_host_id": {hostID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"scope":    {"openid profile email offline_access resource.invoke " + planScope},
		"resource": {c.resource}, "state": {a.state}, "nonce": {a.nonce},
		"code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])},
	}
	if a.previous.ClientID != "" {
		q.Set("client_id", a.previous.ClientID)
		q.Del("agent_name_hint")
		if a.previous.IDToken != "" {
			q.Set("id_token_hint", a.previous.IDToken)
		}
		if a.previous.Email != "" {
			q.Set("login_hint", a.previous.Email)
		}
	}
	return a, c.authorize + "?" + q.Encode(), nil
}

// Finish accepts the local HTTP callback or a callback URL pasted in the CLI
// when Windows cannot forward a WSL loopback port. Both validate the same state,
// callback address, issued client ID, PKCE grant, ID token and plan permission.
func (a *LoginAttempt) Finish(ctx context.Context, callback string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used {
		return ErrAttemptUsed
	}
	u, err := url.Parse(callback)
	want, _ := url.Parse(a.redirect)
	if err != nil || u.Scheme != want.Scheme || u.Host != want.Host || u.Path != want.Path || u.User != nil || u.Fragment != "" {
		return locale.NewError("unexpected callback address")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return locale.NewError("invalid OAuth callback")
	}
	for _, key := range []string{"state", "code", "client_id", "error"} {
		if len(q[key]) > 1 {
			return locale.NewError("duplicate OAuth callback parameter")
		}
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.state)) != 1 {
		return locale.NewError("OAuth state mismatch")
	}
	if q.Get("error") != "" {
		a.used = true
		return locale.NewError("ChatGPT authorization was declined or failed")
	}
	clientID := a.previous.ClientID
	if clientID == "" {
		clientID = q.Get("client_id")
	} else if id := q.Get("client_id"); id != "" && id != clientID {
		return locale.NewError("OAuth client ID mismatch")
	}
	if clientID == "" || clientID == dynamicClient || q.Get("code") == "" {
		return locale.NewError("incomplete ChatGPT client registration")
	}
	a.used = true // authorization codes are single use, including failed exchanges
	c := a.client
	t, err := c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {q.Get("code")}, "code_verifier": {a.verifier}, "redirect_uri": {a.redirect}, "resource": {c.resource}})
	if err != nil {
		return err
	}
	identity, err := c.validateIdentity(ctx, t.IDToken, clientID, a.nonce)
	if err != nil {
		return err
	}
	if a.previous.Subject != "" && identity.Subject != a.previous.Subject {
		return locale.NewError("ChatGPT account identity changed; use a different account label")
	}
	cred, err := c.fromTokens(t, Credentials{Issuer: c.issuer, Subject: identity.Subject, Email: identity.Email, ClientID: clientID, HostID: a.hostID})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.store.Save(c.account, cred); err != nil {
		return err
	}
	c.credentials = cred
	return nil
}

type tokenResponse struct {
	AccessToken     string          `json:"access_token"`
	RefreshToken    string          `json:"refresh_token"`
	IDToken         string          `json:"id_token"`
	TokenType       string          `json:"token_type"`
	Scope           string          `json:"scope"`
	ExpiresIn       int64           `json:"expires_in"`
	EarliestRefresh json.RawMessage `json:"earliest_refresh_at"`
}

// OAuthError deliberately excludes the provider's descriptions and response
// bodies, which may echo an authorization code or token.
type OAuthError struct {
	Code   string
	Status int
}

func (e *OAuthError) Error() string {
	return fmt.Sprintf("ChatGPT OAuth failed: HTTP %d (%s)", e.Status, e.Code)
}

func (c *Client) exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	var t tokenResponse
	r, err := http.NewRequestWithContext(ctx, "POST", c.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return t, locale.NewError("could not build OAuth request")
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(r)
	if err != nil {
		return t, locale.NewError("could not reach ChatGPT token endpoint")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		switch e.Error {
		case "invalid_grant", "invalid_client", "invalid_scope", "access_denied", "temporarily_unavailable", "slow_down":
		default:
			e.Error = "oauth_error"
		}
		return t, &OAuthError{Code: e.Error, Status: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return t, locale.NewError("invalid ChatGPT token response")
	}
	return t, nil
}

func hasScope(scopes []string, scope string) bool {
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func (c *Client) fromTokens(t tokenResponse, old Credentials) (Credentials, error) {
	if t.AccessToken == "" || t.RefreshToken == "" || !strings.EqualFold(t.TokenType, "Bearer") || t.ExpiresIn <= 0 || t.ExpiresIn > 86400 {
		return Credentials{}, locale.NewError("incomplete ChatGPT token response")
	}
	if t.Scope != "" {
		old.Scopes = strings.Fields(t.Scope)
	}
	if !hasScope(old.Scopes, planScope) || !hasScope(old.Scopes, "resource.invoke") || !hasScope(old.Scopes, "offline_access") {
		return Credentials{}, locale.NewError("ChatGPT plan usage was not authorized; sign in again and grant plan access")
	}
	old.AccessToken, old.RefreshToken = t.AccessToken, t.RefreshToken
	if t.IDToken != "" {
		old.IDToken = t.IDToken
	}
	old.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	old.EarliestRefreshAt = time.Time{}
	if len(t.EarliestRefresh) > 0 && string(t.EarliestRefresh) != "null" {
		var epoch int64
		if json.Unmarshal(t.EarliestRefresh, &epoch) == nil {
			old.EarliestRefreshAt = time.Unix(epoch, 0)
		} else {
			var stamp string
			if json.Unmarshal(t.EarliestRefresh, &stamp) != nil {
				return Credentials{}, locale.NewError("invalid refresh time")
			}
			var err error
			old.EarliestRefreshAt, err = time.Parse(time.RFC3339, stamp)
			if err != nil {
				return Credentials{}, locale.NewError("invalid refresh time")
			}
		}
	}
	return old, nil
}

type identityClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`
	Nonce string `json:"nonce"`
}

type discoveryDocument struct {
	Issuer     string `json:"issuer"`
	JWKS       string `json:"jwks_uri"`
	Revocation string `json:"revocation_endpoint"`
}

func (c *Client) getJSON(ctx context.Context, endpoint string, v any) error {
	r, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return locale.NewError("invalid OpenAI discovery endpoint")
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return locale.NewError("could not reach OpenAI discovery endpoint")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return locale.NewError("OpenAI discovery request failed")
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v) != nil {
		return locale.NewError("invalid OpenAI discovery response")
	}
	return nil
}

func (c *Client) discover(ctx context.Context) (discoveryDocument, error) {
	var d discoveryDocument
	if err := c.getJSON(ctx, c.discovery, &d); err != nil {
		return d, err
	}
	if d.Issuer != c.issuer {
		return d, locale.NewError("OpenAI discovery issuer mismatch")
	}
	for _, endpoint := range []string{d.JWKS, d.Revocation} {
		u, err := url.Parse(endpoint)
		base, _ := url.Parse(c.issuer)
		if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Fragment != "" {
			return d, locale.NewError("untrusted OpenAI discovery endpoint")
		}
	}
	return d, nil
}

func (c *Client) validateIdentity(ctx context.Context, raw, clientID, nonce string) (identityClaims, error) {
	var claims identityClaims
	d, err := c.discover(ctx)
	if err != nil {
		return claims, err
	}
	var keys struct {
		Keys []struct{ Kty, Kid, Use, Alg, N, E string } `json:"keys"`
	}
	if err = c.getJSON(ctx, d.JWKS, &keys); err != nil {
		return claims, err
	}
	_, err = jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		for _, k := range keys.Keys {
			if k.Kid != kid || kid == "" || k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
				continue
			}
			n, e1 := base64.RawURLEncoding.DecodeString(k.N)
			e, e2 := base64.RawURLEncoding.DecodeString(k.E)
			if e1 != nil || e2 != nil || len(n) < 256 || len(e) == 0 || len(e) > 4 {
				return nil, locale.NewError("invalid signing key")
			}
			var exponent int64
			for _, b := range e {
				exponent = exponent*256 + int64(b)
			}
			if exponent < 3 || exponent > 2147483647 || exponent%2 == 0 {
				return nil, locale.NewError("invalid signing exponent")
			}
			return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}, nil
		}
		return nil, locale.NewError("OpenAI signing key was not found")
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(c.issuer), jwt.WithAudience(clientID), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || claims.Subject == "" || (nonce != "" && subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1) {
		return identityClaims{}, locale.NewError("ChatGPT ID token validation failed")
	}
	return claims, nil
}

// Account returns only non-secret account information.
func (c *Client) Account() (email, clientID string, signedIn bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credentials.Email, c.credentials.ClientID, c.credentials.RefreshToken != ""
}

func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.credentials.AccessToken == "" || c.credentials.RefreshToken == "" {
		return "", locale.NewError("ChatGPT sign-in required")
	}
	if time.Until(c.credentials.ExpiresAt) > time.Minute {
		return c.credentials.AccessToken, nil
	}
	if time.Now().Before(c.credentials.EarliestRefreshAt) && time.Now().Before(c.credentials.ExpiresAt) {
		return c.credentials.AccessToken, nil
	}
	return c.refreshLocked(ctx)
}

// RefreshAfterUnauthorized avoids multiple rotations when concurrent requests
// receive 401 for the same old access token. Never retry an inference stream.
func (c *Client) RefreshAfterUnauthorized(ctx context.Context, observed string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.credentials.AccessToken != observed && c.credentials.AccessToken != "" {
		return c.credentials.AccessToken, nil
	}
	return c.refreshLocked(ctx)
}

func (c *Client) refreshLocked(ctx context.Context) (string, error) {
	old := c.credentials
	if old.RefreshToken == "" {
		return "", locale.NewError("ChatGPT sign-in required")
	}
	if time.Now().Before(old.EarliestRefreshAt) {
		return "", locale.NewError("ChatGPT token cannot be refreshed yet")
	}
	t, err := c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {old.ClientID}, "refresh_token": {old.RefreshToken}, "resource": {c.resource}})
	if err != nil {
		var oauth *OAuthError
		if errors.As(err, &oauth) && (oauth.Code == "invalid_grant" || oauth.Code == "invalid_client") {
			old.AccessToken, old.RefreshToken, old.IDToken = "", "", ""
			c.credentials = old
			if e := c.store.Save(c.account, old); e != nil {
				return "", locale.NewError("ChatGPT session expired and could not be cleared from disk")
			}
		}
		return "", err
	}
	// Persist a rotated refresh token even if another validation fails: keeping
	// the old single-use token on disk would make subsequent login impossible.
	if t.IDToken != "" {
		identity, e := c.validateIdentity(ctx, t.IDToken, old.ClientID, "")
		if e != nil || identity.Subject != old.Subject {
			old.AccessToken, old.RefreshToken, old.IDToken = "", "", ""
			c.credentials = old
			_ = c.store.Save(c.account, old)
			return "", locale.NewError("refreshed ChatGPT identity validation failed; sign in again")
		}
		old.Email = identity.Email
	}
	next, err := c.fromTokens(t, old)
	if err != nil {
		old.AccessToken, old.RefreshToken, old.IDToken = "", "", ""
		c.credentials = old
		_ = c.store.Save(c.account, old)
		return "", err
	}
	// Keep the latest token in memory if persistence fails, but do not send an
	// inference request until the durable credential record has been written.
	c.credentials = next
	if err = c.store.Save(c.account, next); err != nil {
		c.credentials.AccessToken = ""
		return "", locale.NewError("could not persist renewed ChatGPT credentials; sign in again")
	}
	return next.AccessToken, nil
}

// Logout attempts remote revocation, then clears tokens while retaining the
// validated identity/client mapping and stable host ID for a later sign-in.
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.credentials
	var remoteErr error
	if old.RefreshToken != "" {
		d, err := c.discover(ctx)
		if err != nil {
			remoteErr = err
		} else {
			form := url.Values{"token": {old.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {old.ClientID}}
			for i := 0; i < 3; i++ {
				r, _ := http.NewRequestWithContext(ctx, "POST", d.Revocation, strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				resp, e := c.http.Do(r)
				if e == nil {
					resp.Body.Close()
					if resp.StatusCode == 200 {
						remoteErr = nil
						break
					}
					if resp.StatusCode < 500 {
						remoteErr = locale.NewError("remote ChatGPT revocation was not confirmed")
						break
					}
				}
				remoteErr = locale.NewError("remote ChatGPT revocation was not confirmed")
				if i < 2 {
					select {
					case <-ctx.Done():
						i = 3
					case <-time.After(time.Duration(i+1) * time.Second):
					}
				}
			}
		}
	}
	old.AccessToken, old.RefreshToken, old.IDToken = "", "", ""
	c.credentials = old
	if err := c.store.Save(c.account, old); err != nil {
		return locale.NewError("could not clear local ChatGPT credentials")
	}
	if remoteErr != nil {
		return locale.NewError("local tokens cleared; remote revocation was not confirmed; disconnect the app in ChatGPT Settings")
	}
	return nil
}
