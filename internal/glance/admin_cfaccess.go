package glance

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type cfAccessConfig struct {
	TeamDomain    string
	Audience      string
	AllowedEmails []string
	// InsecureHTTP allows http:// JWKS URLs. Only set in tests.
	InsecureHTTP bool
}

type cfAccessVerifier struct {
	cfg      cfAccessConfig
	verifier *oidc.IDTokenVerifier
	allowed  map[string]struct{} // lowercase emails for O(1) lookup
}

func newCFAccessVerifier(ctx context.Context, cfg cfAccessConfig) (*cfAccessVerifier, error) {
	if cfg.TeamDomain == "" {
		return nil, fmt.Errorf("cloudflare-access.team-domain is required")
	}
	if cfg.Audience == "" {
		return nil, fmt.Errorf("cloudflare-access.audience is required")
	}
	if len(cfg.AllowedEmails) == 0 {
		return nil, fmt.Errorf("cloudflare-access.allowed-emails must list at least one address")
	}

	scheme := "https"
	if cfg.InsecureHTTP {
		scheme = "http"
	}
	certsURL := fmt.Sprintf("%s://%s/cdn-cgi/access/certs", scheme, cfg.TeamDomain)

	keySet := oidc.NewRemoteKeySet(ctx, certsURL)
	verifier := oidc.NewVerifier(
		fmt.Sprintf("%s://%s", scheme, cfg.TeamDomain),
		keySet,
		&oidc.Config{
			ClientID:             cfg.Audience,
			SkipClientIDCheck:    false,
			SkipIssuerCheck:      true, // CF Access tokens don't use a standard iss
			SupportedSigningAlgs: []string{"RS256"},
		},
	)

	allowed := make(map[string]struct{}, len(cfg.AllowedEmails))
	for _, e := range cfg.AllowedEmails {
		allowed[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}

	return &cfAccessVerifier{cfg: cfg, verifier: verifier, allowed: allowed}, nil
}

func (v *cfAccessVerifier) verify(ctx context.Context, rawToken string) (email string, err error) {
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", fmt.Errorf("jwt verification: %w", err)
	}

	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := tok.Claims(&claims); err != nil {
		return "", fmt.Errorf("parsing claims: %w", err)
	}
	if claims.Email == "" {
		return "", fmt.Errorf("jwt has no email claim")
	}

	if _, ok := v.allowed[strings.ToLower(claims.Email)]; !ok {
		return "", fmt.Errorf("email %q not in allowlist", claims.Email)
	}

	return claims.Email, nil
}

// verifyFromRequest is a convenience wrapper that extracts the header.
func (v *cfAccessVerifier) verifyFromRequest(r *http.Request) (email string, err error) {
	tok := r.Header.Get("Cf-Access-Jwt-Assertion")
	if tok == "" {
		return "", fmt.Errorf("missing Cf-Access-Jwt-Assertion header")
	}
	return v.verify(r.Context(), tok)
}

// verifiedEmailKey is the unexported context key under which the middleware
// stashes the email extracted from a verified Cf-Access-Jwt-Assertion. Reading
// from context is the only trustworthy way to learn the caller's identity;
// raw Cf-Access-* headers are attacker-controllable if CF Access is ever
// bypassed at the network edge.
type verifiedEmailKey struct{}

func withVerifiedEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, verifiedEmailKey{}, email)
}

func verifiedEmailFromContext(ctx context.Context) (string, bool) {
	email, ok := ctx.Value(verifiedEmailKey{}).(string)
	return email, ok && email != ""
}
