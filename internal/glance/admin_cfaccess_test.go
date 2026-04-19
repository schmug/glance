package glance

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// testJWKSServer returns a *httptest.Server that serves a JWKS containing the
// supplied RSA public key, along with the signer used to mint tokens.
func testJWKSServer(t *testing.T, key *rsa.PrivateKey, kid string) (*httptest.Server, jose.Signer) {
	t.Helper()

	jwkSet := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"use": "sig",
			"kid": kid,
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn-cgi/access/certs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwkSet)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return srv, signer
}

func mintToken(t *testing.T, signer jose.Signer, audience, email string, expiry time.Time) string {
	t.Helper()
	claims := map[string]any{
		"aud":   audience,
		"email": email,
		"exp":   expiry.Unix(),
		"iat":   time.Now().Unix(),
	}
	tok, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

func TestCFAccessVerifier_AcceptsValidToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	srv, signer := testJWKSServer(t, key, "test-kid")
	teamDomain := strings.TrimPrefix(srv.URL, "http://")
	aud := "abc123"

	v, err := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain:    teamDomain,
		Audience:      aud,
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}

	tok := mintToken(t, signer, aud, "you@example.com", time.Now().Add(time.Hour))
	email, err := v.verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.EqualFold(email, "you@example.com") {
		t.Fatalf("email: got %q", email)
	}
}

func TestCFAccessVerifier_RejectsWrongAudience(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, signer := testJWKSServer(t, key, "k1")
	v, _ := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain:    strings.TrimPrefix(srv.URL, "http://"),
		Audience:      "expected-aud",
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})

	tok := mintToken(t, signer, "wrong-aud", "you@example.com", time.Now().Add(time.Hour))
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected error for wrong audience")
	}
}

func TestCFAccessVerifier_RejectsEmailNotInAllowlist(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, signer := testJWKSServer(t, key, "k1")
	v, _ := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain:    strings.TrimPrefix(srv.URL, "http://"),
		Audience:      "abc",
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})

	tok := mintToken(t, signer, "abc", "other@example.com", time.Now().Add(time.Hour))
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected error for email not in allowlist")
	}
}

func TestCFAccessVerifier_RejectsExpiredToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv, signer := testJWKSServer(t, key, "k1")
	v, _ := newCFAccessVerifier(context.Background(), cfAccessConfig{
		TeamDomain:    strings.TrimPrefix(srv.URL, "http://"),
		Audience:      "abc",
		AllowedEmails: []string{"you@example.com"},
		InsecureHTTP:  true,
	})

	tok := mintToken(t, signer, "abc", "you@example.com", time.Now().Add(-time.Minute))
	if _, err := v.verify(context.Background(), tok); err == nil {
		t.Fatal("expected error for expired token")
	}
}
