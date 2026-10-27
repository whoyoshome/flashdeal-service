package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWKSConfig configures RS256 verification against an OIDC JWKS endpoint.
type JWKSConfig struct {
	// URL is the JWKS document (for example https://issuer/.well-known/jwks.json).
	URL string
	// Issuer must match the iss claim on every accepted token.
	Issuer string
	// HTTPClient fetches the JWKS document. Nil uses a client with a 5s timeout.
	HTTPClient *http.Client
	// CacheTTL is how long fetched keys are reused. Zero means 5 minutes.
	CacheTTL time.Duration
}

// JWKSVerifier verifies Bearer JWTs signed with RS256 keys published at a
// JWKS URL. It is the production-shaped alternative to Service (HS256).
type JWKSVerifier struct {
	url    string
	issuer string
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// NewJWKS validates cfg and returns a verifier that loads keys on demand.
func NewJWKS(cfg JWKSConfig) (*JWKSVerifier, error) {
	if cfg.URL == "" {
		return nil, errors.New("jwt jwks url is required")
	}
	if cfg.Issuer == "" {
		return nil, errors.New("jwt issuer is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &JWKSVerifier{
		url:    cfg.URL,
		issuer: cfg.Issuer,
		client: client,
		ttl:    ttl,
		now:    time.Now,
		keys:   map[string]*rsa.PublicKey{},
	}, nil
}

// ParseAuthorization extracts and verifies a Bearer RS256 JWT.
func (v *JWKSVerifier) ParseAuthorization(header string) (Identity, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return Identity{}, ErrInvalidToken
	}
	timeout := v.client.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return v.Parse(ctx, strings.TrimSpace(header[len(prefix):]))
}

// Parse verifies a raw RS256 JWT against the cached JWKS.
func (v *JWKSVerifier) Parse(ctx context.Context, raw string) (Identity, error) {
	if raw == "" {
		return Identity{}, ErrInvalidToken
	}
	parsed, err := jwt.ParseWithClaims(raw, &claims{}, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("%w: unexpected alg", ErrInvalidToken)
		}
		kid, _ := t.Header["kid"].(string)
		key, err := v.key(ctx, kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(v.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil || !parsed.Valid {
		return Identity{}, ErrInvalidToken
	}
	c, ok := parsed.Claims.(*claims)
	if !ok || !ValidSubject(c.Subject) || !Role(c.Role).valid() {
		return Identity{}, ErrInvalidToken
	}
	return Identity{Subject: c.Subject, Role: Role(c.Role)}, nil
}

func (v *JWKSVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if key := v.cached(kid); key != nil {
		return key, nil
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	if key := v.cached(kid); key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("%w: unknown kid", ErrInvalidToken)
}

func (v *JWKSVerifier) cached(kid string) *rsa.PublicKey {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.now().Sub(v.fetched) > v.ttl {
		return nil
	}
	return v.keys[kid]
}

func (v *JWKSVerifier) refresh(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.now().Sub(v.fetched) <= v.ttl && len(v.keys) > 0 {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return fmt.Errorf("jwks request: %w", err)
	}
	res, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch: status %d", res.StatusCode)
	}
	var doc jwksDocument
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return fmt.Errorf("jwks decode: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" || k.N == "" || k.E == "" {
			continue
		}
		pub, err := rsaPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("jwks: no usable RSA keys")
	}
	v.keys = keys
	v.fetched = v.now()
	return nil
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

func rsaPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	var eInt int
	for _, b := range eBytes {
		eInt = eInt<<8 + int(b)
	}
	if eInt == 0 {
		return nil, errors.New("invalid exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: eInt}, nil
}
