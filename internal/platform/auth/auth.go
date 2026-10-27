// Package auth issues and validates HS256 JWTs used as Bearer credentials.
// In production an IdP (OAuth 2.0 / OIDC) would issue the tokens; this
// package is the verification boundary the HTTP layer depends on.
package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
)

// Role is a claim that drives authorization. Values are part of the token
// contract and must not change.
type Role string

// Roles recognized by the service.
const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// MinimumSecretLen rejects short HMAC keys that are trivial to brute-force.
const MinimumSecretLen = 32

// MaxSubjectLen bounds the JWT subject (user id) so oversized values
// never reach storage or logs.
const MaxSubjectLen = 128

// ErrInvalidToken means the Bearer credential is missing, malformed,
// expired, or signed with the wrong key/algorithm.
var ErrInvalidToken = errors.New("invalid token")

// Config holds the HMAC settings shared by issuers and verifiers.
type Config struct {
	// Secret is the HS256 key. Never log it.
	Secret string
	// Issuer is written into and required on every token (iss claim).
	Issuer string
}

// Identity is what a verified token asserts about the caller.
type Identity struct {
	Subject string
	Role    Role
}

// Service issues and verifies JWTs with one shared secret.
type Service struct {
	secret []byte
	issuer string
	now    func() time.Time
}

// New validates cfg and returns a Service. Secret must be at least
// MinimumSecretLen bytes; issuer must be non-empty.
func New(cfg Config) (*Service, error) {
	if len(cfg.Secret) < MinimumSecretLen {
		return nil, fmt.Errorf("jwt secret must be at least %d bytes", MinimumSecretLen)
	}
	if cfg.Issuer == "" {
		return nil, errors.New("jwt issuer is required")
	}
	return &Service{secret: []byte(cfg.Secret), issuer: cfg.Issuer, now: time.Now}, nil
}

// Issue mints a signed JWT for subject with role, valid for ttl from now.
func (s *Service) Issue(subject string, role Role, ttl time.Duration) (string, error) {
	if !ValidSubject(subject) {
		return "", fmt.Errorf("%w: subject", ErrInvalidToken)
	}
	if !role.valid() {
		return "", fmt.Errorf("%w: role", ErrInvalidToken)
	}
	if ttl <= 0 {
		return "", fmt.Errorf("%w: ttl must be positive", ErrInvalidToken)
	}
	now := s.now().UTC()
	claims := claims{
		Role: string(role),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// Parse verifies a raw JWT (without the "Bearer " prefix) and returns the
// identity it carries.
func (s *Service) Parse(raw string) (Identity, error) {
	if raw == "" {
		return Identity{}, ErrInvalidToken
	}
	parsed, err := jwt.ParseWithClaims(raw, &claims{}, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("%w: unexpected alg", ErrInvalidToken)
		}
		return s.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(s.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(s.now),
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

// ParseAuthorization extracts and verifies a Bearer token from an
// Authorization header value.
func (s *Service) ParseAuthorization(header string) (Identity, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return Identity{}, ErrInvalidToken
	}
	return s.Parse(strings.TrimSpace(header[len(prefix):]))
}

// ValidSubject reports whether subject is a usable user id.
func ValidSubject(subject string) bool {
	if subject == "" || len(subject) > MaxSubjectLen {
		return false
	}
	for _, c := range subject {
		if !unicode.IsPrint(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}

func (r Role) valid() bool {
	return r == RoleUser || r == RoleAdmin
}

// claims is the JWT payload. Role is a private claim; the rest are registered.
type claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}
