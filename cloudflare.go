package main

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type CloudflareConfig struct {
	Team     string `json:"team"`
	Audience string `json:"audience"`
	Email    string `json:"email"`
	Hostname string `json:"hostname"`
}

type cloudflareAccess struct {
	config  CloudflareConfig
	issuer  string
	parser  *jwt.Parser
	client  *http.Client
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

var cloudflareTeamPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func newCloudflareAccess(config CloudflareConfig) (*cloudflareAccess, error) {
	if !cloudflareTeamPattern.MatchString(config.Team) || config.Audience == "" || config.Email == "" || config.Hostname == "" {
		return nil, errors.New("invalid Cloudflare Access settings")
	}
	issuer := "https://" + config.Team + ".cloudflareaccess.com"
	return &cloudflareAccess{
		config: config, issuer: issuer,
		parser: jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(config.Audience), jwt.WithExpirationRequired()),
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (a *cloudflareAccess) refresh(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.issuer+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("Cloudflare signing keys unavailable")
	}
	var jwks struct {
		Keys []struct {
			ID       string `json:"kid"`
			Type     string `json:"kty"`
			Modulus  string `json:"n"`
			Exponent string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&jwks); err != nil {
		return err
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, key := range jwks.Keys {
		modulus, modErr := base64.RawURLEncoding.DecodeString(key.Modulus)
		exponent, expErr := base64.RawURLEncoding.DecodeString(key.Exponent)
		if key.Type != "RSA" || key.ID == "" || modErr != nil || expErr != nil || len(exponent) > 4 {
			continue
		}
		n, e := new(big.Int).SetBytes(modulus), new(big.Int).SetBytes(exponent).Int64()
		if n.BitLen() >= 2048 && e >= 3 && e <= 1<<31-1 && e%2 == 1 {
			keys[key.ID] = &rsa.PublicKey{N: n, E: int(e)}
		}
	}
	if len(keys) == 0 {
		return errors.New("Cloudflare Access returned no signing keys")
	}
	a.keys, a.fetched = keys, time.Now()
	return nil
}

func (a *cloudflareAccess) key(ctx context.Context, token *jwt.Token) (any, error) {
	id, ok := token.Header["kid"].(string)
	if !ok || id == "" {
		return nil, errors.New("missing Cloudflare signing key ID")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	age := time.Since(a.fetched)
	if age >= time.Hour || (a.keys[id] == nil && age >= 30*time.Second) {
		if err := a.refresh(ctx); err != nil {
			return nil, err
		}
	}
	if key := a.keys[id]; key != nil {
		return key, nil
	}
	return nil, errors.New("unknown Cloudflare signing key")
}

func (a *cloudflareAccess) authorized(request *http.Request) bool {
	if !strings.EqualFold(request.Host, a.config.Hostname) {
		return false
	}
	values := request.Header.Values("Cf-Access-Jwt-Assertion")
	if len(values) != 1 || len(values[0]) > 16<<10 {
		return false
	}
	claims := struct {
		Email string `json:"email"`
		Kind  string `json:"type"`
		jwt.RegisteredClaims
	}{}
	token, err := a.parser.ParseWithClaims(values[0], &claims, func(token *jwt.Token) (any, error) {
		return a.key(request.Context(), token)
	})
	return err == nil && token.Valid && claims.Kind == "app" && strings.EqualFold(claims.Email, a.config.Email)
}
