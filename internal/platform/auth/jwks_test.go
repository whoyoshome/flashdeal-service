package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
)

func TestJWKSVerifier(t *testing.T) {
	t.Parallel()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	const kid = "test-key-1"
	const issuer = "https://idp.example/realms/flashdeal"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": kid,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}},
		})
	}))
	t.Cleanup(srv.Close)

	v, err := auth.NewJWKS(auth.JWKSConfig{URL: srv.URL, Issuer: issuer, HTTPClient: srv.Client()})
	require.NoError(t, err)

	t.Run("accepts a valid RS256 token from the JWKS", func(t *testing.T) {
		tok := signRS256(t, key, kid, issuer, "alice", "user", time.Hour)
		id, err := v.ParseAuthorization("Bearer " + tok)
		require.NoError(t, err)
		require.Equal(t, "alice", id.Subject)
		require.Equal(t, auth.RoleUser, id.Role)
	})

	t.Run("rejects HS256 even when the claims look valid", func(t *testing.T) {
		hs, err := auth.New(auth.Config{Secret: "test-secret-at-least-32-bytes-long!!", Issuer: issuer})
		require.NoError(t, err)
		tok, err := hs.Issue("alice", auth.RoleUser, time.Hour)
		require.NoError(t, err)
		_, err = v.ParseAuthorization("Bearer " + tok)
		require.ErrorIs(t, err, auth.ErrInvalidToken)
	})

	t.Run("rejects an unknown kid", func(t *testing.T) {
		tok := signRS256(t, key, "other-kid", issuer, "alice", "user", time.Hour)
		_, err := v.ParseAuthorization("Bearer " + tok)
		require.ErrorIs(t, err, auth.ErrInvalidToken)
	})

	t.Run("rejects a missing Bearer prefix", func(t *testing.T) {
		_, err := v.ParseAuthorization("alice")
		require.ErrorIs(t, err, auth.ErrInvalidToken)
	})
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid, issuer, sub, role string, ttl time.Duration) string {
	t.Helper()
	now := time.Now().UTC()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"role": role,
		"sub":  sub,
		"iss":  issuer,
		"iat":  now.Unix(),
		"exp":  now.Add(ttl).Unix(),
	})
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	require.NoError(t, err)
	return signed
}
