// Package security implements the Authentication service's GLOBAL JWT contract.
package security

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const MaxAccessTokenLifetime = 15 * time.Minute

var ErrInvalidToken = errors.New("invalid access token")

type Claims struct {
	UserID uuid.UUID `json:"user_id"`
	Email  string    `json:"email"`
	// A pointer distinguishes a missing/null version from the valid version zero.
	TokenVersion *int32   `json:"token_version"`
	Permissions  []string `json:"permissions"`
	jwt.RegisteredClaims
}

// NumericDate must be a JSON number; the JWT library also accepts numeric
// strings, which are not emitted by Auth and are outside this claim contract.
func (c *Claims) UnmarshalJSON(data []byte) error {
	type plainClaims Claims
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"iat", "nbf", "exp"} {
		var number any
		if err := json.Unmarshal(fields[name], &number); err != nil {
			return ErrInvalidToken
		}
		if _, ok := number.(float64); !ok {
			return ErrInvalidToken
		}
	}
	return json.Unmarshal(data, (*plainClaims)(c))
}

func (c Claims) Validate() error {
	if c.UserID == uuid.Nil || c.Subject != c.UserID.String() || c.TokenVersion == nil || *c.TokenVersion < 0 || c.Permissions == nil {
		return ErrInvalidToken
	}
	for _, p := range c.Permissions {
		parts := strings.Split(p, ":")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.TrimSpace(parts[0]) != parts[0] || strings.TrimSpace(parts[1]) != parts[1] {
			return ErrInvalidToken
		}
	}
	if c.IssuedAt == nil || c.NotBefore == nil || c.ExpiresAt == nil {
		return ErrInvalidToken
	}
	iat, nbf, exp := c.IssuedAt.Time, c.NotBefore.Time, c.ExpiresAt.Time
	if !exp.After(iat) || nbf.Before(iat) || !exp.After(nbf) || exp.Sub(iat) > MaxAccessTokenLifetime {
		return ErrInvalidToken
	}
	return nil
}

type Verifier struct {
	key              *rsa.PublicKey
	issuer, audience string
}

// Load accepts only public-key PEM (PKIX or PKCS#1), never a private key.
func Load(path, issuer, audience string) (*Verifier, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(issuer) == "" || issuer != strings.TrimSpace(issuer) || strings.TrimSpace(audience) == "" || audience != strings.TrimSpace(audience) {
		return nil, errors.New("JWT_PUBLIC_KEY_PATH, JWT_ISSUER and JWT_AUDIENCE are required without blank values or surrounding issuer/audience whitespace")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read JWT public key")
	}
	block, rest := pem.Decode(data)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 || len(block.Headers) != 0 {
		return nil, errors.New("JWT public key must contain a single public PEM block")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "-----BEGIN "+block.Type+"-----") {
		return nil, errors.New("unexpected content before JWT public key")
	}
	var key *rsa.PublicKey
	switch block.Type {
	case "PUBLIC KEY":
		parsed, parseErr := x509.ParsePKIXPublicKey(block.Bytes)
		if parseErr == nil {
			key, _ = parsed.(*rsa.PublicKey)
		}
	case "RSA PUBLIC KEY":
		key, _ = x509.ParsePKCS1PublicKey(block.Bytes)
	}
	if key == nil || key.N == nil || key.N.BitLen() < 2048 || key.N.Sign() <= 0 || key.N.Bit(0) == 0 || key.E < 3 || key.E%2 == 0 {
		return nil, errors.New("JWT public key must be RSA with at least 2048 bits")
	}
	return &Verifier{key: key, issuer: issuer, audience: audience}, nil
}

func (v *Verifier) Verify(raw string) (*Claims, error) {
	if v == nil || v.key == nil {
		return nil, ErrInvalidToken
	}
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodRS256 {
			return nil, ErrInvalidToken
		}
		return v.key, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer(v.issuer), jwt.WithAudience(v.audience), jwt.WithIssuedAt())
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
