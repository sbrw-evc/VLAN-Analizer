package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
)

func TestEntraSignedTokensAndRoles(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	tenant := "11111111-1111-1111-1111-111111111111"
	client := "22222222-2222-2222-2222-222222222222"
	var raw, issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(Object{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "ci", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			r.ParseForm()
			if r.Form.Get("code_verifier") != "test-verifier" {
				t.Error("PKCE verifier missing")
			}
			json.NewEncoder(w).Encode(Object{"access_token": "unused", "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	provider, e := oidc.NewProvider(context.Background(), issuer)
	if e != nil {
		t.Fatal(e)
	}
	auth := NewAuth(newMemory(), randomKey(64))
	auth.provider = provider
	auth.tenant = tenant
	config := EntraConfig{Enabled: true, Tenant: tenant, ClientID: client, Secret: "ci-secret"}
	cookie, e := auth.Tokens.Sign(Object{"state": "test-state", "nonce": "test-nonce", "verifier": "test-verifier"}, "vlan-oidc", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	for _, scenario := range []string{"admin", "viewer", "no-role", "wrong-nonce", "wrong-audience", "wrong-issuer", "wrong-tenant", "expired", "wrong-state"} {
		t.Run(scenario, func(t *testing.T) {
			claims := jwt.MapClaims{"iss": issuer, "sub": "subject", "aud": client, "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "tid": tenant, "oid": "object-id", "nonce": "test-nonce", "roles": []string{"VLAN.Admin"}}
			state := "test-state"
			switch scenario {
			case "viewer":
				claims["roles"] = []string{"VLAN.Viewer"}
			case "no-role":
				claims["roles"] = []string{"Unrelated.Role"}
			case "wrong-nonce":
				claims["nonce"] = "other"
			case "wrong-audience":
				claims["aud"] = "other"
			case "wrong-issuer":
				claims["iss"] = "https://other.invalid"
			case "wrong-tenant":
				claims["tid"] = "other"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Minute).Unix()
			case "wrong-state":
				state = "other"
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "ci"
			raw, e = token.SignedString(key)
			if e != nil {
				t.Fatal(e)
			}
			session, err := auth.EntraCallback(context.Background(), config, "http://localhost:8000", "code", state, cookie)
			if scenario == "admin" || scenario == "viewer" {
				if err != nil {
					t.Fatal(err)
				}
				parsed, e := auth.Tokens.Parse(session, "vlan-session")
				if e != nil || parsed["role"] != scenario {
					t.Fatal("role mapping", e)
				}
			} else if err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}
