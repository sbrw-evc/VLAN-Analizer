package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
	"golang.org/x/oauth2"
)

type TokenService struct{ Key []byte }

func (t TokenService) Sign(payload Object, aud string, ttl time.Duration) (string, error) {
	claims := jwt.MapClaims{}
	for k, v := range payload {
		claims[k] = v
	}
	claims["aud"] = aud
	claims["iat"] = time.Now().Unix()
	claims["exp"] = time.Now().Add(ttl).Unix()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.Key)
}
func (t TokenService) Parse(token, aud string) (jwt.MapClaims, error) {
	p, e := jwt.Parse(token, func(token *jwt.Token) (any, error) { return t.Key, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience(aud), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithJSONNumber())
	if e != nil || !p.Valid {
		return nil, errors.New("invalid or expired session")
	}
	return p.Claims.(jwt.MapClaims), nil
}
func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must contain 12–1024 bytes")
	}
	salt := []byte(randomKey(16))
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}
func VerifyPassword(encoded, password string) bool {
	if len(password) > 1024 {
		return false
	}
	p := strings.Split(encoded, "$")
	if len(p) != 6 || p[1] != "argon2id" || p[2] != "v=19" {
		return false
	}
	var memory, iterations uint32
	var parallel uint8
	if _, e := fmt.Sscanf(p[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallel); e != nil || memory > 256*1024 || memory < 8 || iterations < 1 || iterations > 10 || parallel < 1 || parallel > 16 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(p[4])
	if e != nil {
		return false
	}
	expected, e := base64.RawStdEncoding.DecodeString(p[5])
	if e != nil || len(expected) < 16 || len(expected) > 64 {
		return false
	}
	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallel, uint32(len(expected)))
	return subtle.ConstantTimeCompare(hash, expected) == 1
}
func newUser(name, password, role string, disabled bool) (User, error) {
	if !namePattern.MatchString(name) || (role != "admin" && role != "viewer") {
		return User{}, errors.New("invalid username or role")
	}
	hash, e := HashPassword(password)
	return User{hash, role, disabled, time.Now().UnixNano()}, e
}

type AuthService struct {
	Store      SecretStore
	Tokens     TokenService
	mu         sync.Mutex
	providerMu sync.Mutex
	attempts   map[string][]time.Time
	hashSlots  chan struct{}
	provider   *oidc.Provider
	tenant     string
}

func NewAuth(store SecretStore, key string) *AuthService {
	return &AuthService{Store: store, Tokens: TokenService{[]byte(key)}, attempts: map[string][]time.Time{}, hashSlots: make(chan struct{}, 4)}
}
func (a *AuthService) AllowLogin(address string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	for key, items := range a.attempts {
		if len(items) == 0 || items[len(items)-1].Before(cutoff) {
			delete(a.attempts, key)
		}
	}
	if len(a.attempts) > 10000 {
		return false
	}
	list := []time.Time{}
	for _, t := range a.attempts[address] {
		if t.After(cutoff) {
			list = append(list, t)
		}
	}
	if len(list) >= 10 {
		return false
	}
	a.attempts[address] = append(list, time.Now())
	return true
}
func (a *AuthService) Local(ctx context.Context, name, password string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", errors.New("invalid credentials")
	}
	select {
	case a.hashSlots <- struct{}{}:
		defer func() { <-a.hashSlots }()
	default:
		return "", errors.New("authentication capacity exceeded")
	}
	var user User
	found, e := a.Store.Read(ctx, "users/"+name, &user)
	if e != nil {
		return "", e
	}
	if !found || user.Disabled {
		_, _ = HashPassword("constant-time-unknown-password")
		return "", errors.New("invalid credentials")
	}
	if !VerifyPassword(user.PasswordHash, password) {
		return "", errors.New("invalid credentials")
	}
	return a.Tokens.Sign(Object{"sub": name, "provider": "local", "role": user.Role, "version": user.Version}, "vlan-session", 8*time.Hour)
}
func (a *AuthService) User(ctx context.Context, r *http.Request, entraEnabled bool) (Session, error) {
	cookie, e := r.Cookie("session")
	if e != nil {
		return Session{}, errors.New("sign in required")
	}
	claims, e := a.Tokens.Parse(cookie.Value, "vlan-session")
	if e != nil {
		return Session{}, e
	}
	u := Session{Sub: text(claims["sub"]), Name: text(claims["name"]), Provider: text(claims["provider"]), Role: text(claims["role"])}
	if u.Provider == "local" {
		var account User
		found, e := a.Store.Read(ctx, "users/"+u.Sub, &account)
		if e != nil || !found || account.Disabled || fmt.Sprint(account.Version) != text(claims["version"]) {
			return Session{}, errors.New("account or session revoked")
		}
		u.Role = account.Role
	} else if u.Provider != "entra" || !entraEnabled {
		return Session{}, errors.New("authentication provider disabled")
	}
	return u, nil
}
func sessionCookie(w http.ResponseWriter, name, value, origin string, ttl int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: ttl, HttpOnly: true, Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteLaxMode})
}
func (a *AuthService) oidc(ctx context.Context, c EntraConfig) (*oidc.Provider, error) {
	a.providerMu.Lock()
	defer a.providerMu.Unlock()
	if a.provider != nil && a.tenant == c.Tenant {
		return a.provider, nil
	}
	// Cached JWKS refreshes outlive the initiating HTTP request.
	ctx = oidc.ClientContext(context.WithoutCancel(ctx), httpClient(15*time.Second))
	p, e := oidc.NewProvider(ctx, "https://login.microsoftonline.com/"+c.Tenant+"/v2.0")
	if e != nil {
		return nil, errors.New("Entra discovery failed")
	}
	a.provider = p
	a.tenant = c.Tenant
	return p, nil
}
func (a *AuthService) EntraStart(ctx context.Context, c EntraConfig, origin string) (string, string, error) {
	if !c.Enabled {
		return "", "", errors.New("Entra is disabled")
	}
	if e := c.Validate(); e != nil {
		return "", "", e
	}
	provider, e := a.oidc(ctx, c)
	if e != nil {
		return "", "", e
	}
	state, nonce, verifier := randomKey(32), randomKey(32), oauth2.GenerateVerifier()
	flow, e := a.Tokens.Sign(Object{"state": state, "nonce": nonce, "verifier": verifier}, "vlan-oidc", 10*time.Minute)
	if e != nil {
		return "", "", e
	}
	config := oauth2.Config{ClientID: c.ClientID, ClientSecret: c.Secret, Endpoint: provider.Endpoint(), RedirectURL: origin + "/auth/entra/callback", Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	return config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), flow, nil
}
func (a *AuthService) EntraCallback(ctx context.Context, c EntraConfig, origin, code, state, cookie string) (string, error) {
	flow, e := a.Tokens.Parse(cookie, "vlan-oidc")
	if e != nil || !hmac.Equal([]byte(text(flow["state"])), []byte(state)) {
		return "", errors.New("OIDC state mismatch")
	}
	provider, e := a.oidc(ctx, c)
	if e != nil {
		return "", e
	}
	config := oauth2.Config{ClientID: c.ClientID, ClientSecret: c.Secret, Endpoint: provider.Endpoint(), RedirectURL: origin + "/auth/entra/callback"}
	ctx = oidc.ClientContext(ctx, httpClient(15*time.Second))
	token, e := config.Exchange(ctx, code, oauth2.VerifierOption(text(flow["verifier"])))
	if e != nil {
		return "", errors.New("Entra token exchange failed")
	}
	raw, _ := token.Extra("id_token").(string)
	verified, e := provider.Verifier(&oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{"RS256"}}).Verify(ctx, raw)
	if e != nil || !hmac.Equal([]byte(verified.Nonce), []byte(text(flow["nonce"]))) {
		return "", errors.New("invalid Entra token")
	}
	var claims struct {
		Tenant string   `json:"tid"`
		OID    string   `json:"oid"`
		Name   string   `json:"name"`
		Roles  []string `json:"roles"`
	}
	if e = verified.Claims(&claims); e != nil || claims.Tenant != c.Tenant || claims.OID == "" {
		return "", errors.New("invalid Entra tenant")
	}
	role := ""
	for _, r := range claims.Roles {
		if r == "VLAN.Admin" {
			role = "admin"
			break
		}
		if r == "VLAN.Viewer" {
			role = "viewer"
		}
	}
	if role == "" {
		return "", errors.New("assign VLAN.Admin or VLAN.Viewer")
	}
	return a.Tokens.Sign(Object{"sub": claims.OID, "name": claims.Name, "provider": "entra", "role": role}, "vlan-session", 8*time.Hour)
}
func remoteIP(r *http.Request) string {
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return r.RemoteAddr
	}
	return ip
}
func digest(s string) string { d := sha256.Sum256([]byte(s)); return fmt.Sprintf("%x", d) }
