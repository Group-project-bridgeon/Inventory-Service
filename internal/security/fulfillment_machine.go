package security

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"io"
	"strings"
	"time"

	"context"
	f "github.com/Group-project-bridgeon/Inventory-Service/internal/fulfillment"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const FulfillmentMachineAudience = f.Audience

var fulfillmentPermissions = map[string]bool{f.CreatePermission: true, f.CancelPermission: true, f.ReadPermission: true}

const FulfillmentMachineMaxLifetime = 5 * time.Minute
const FulfillmentMachineClockTolerance = 30 * time.Second

type FulfillmentMachineClaims struct {
	ClientID    string   `json:"client_id"`
	Kind        string   `json:"token_kind"`
	Permissions []string `json:"permissions"`
	jwt.RegisteredClaims
}

// Reject duplicate keys and require exact integer NumericDates before jwt/v5's
// more permissive decoder. Unknown claims are rejected for this versioned contract.
func (c *FulfillmentMachineClaims) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return ErrInvalidToken
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return ErrInvalidToken
		}
		name, ok := key.(string)
		if !ok {
			return ErrInvalidToken
		}
		if _, exists := fields[name]; exists {
			return ErrInvalidToken
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return ErrInvalidToken
		}
		fields[name] = value
	}
	if _, err = dec.Token(); err != nil {
		return ErrInvalidToken
	}
	if _, err = dec.Token(); err != io.EOF {
		return ErrInvalidToken
	}
	if len(fields) != 10 {
		return ErrInvalidToken
	}
	for _, name := range []string{"iss", "sub", "client_id", "token_kind", "jti"} {
		var value string
		if json.Unmarshal(fields[name], &value) != nil || value == "" {
			return ErrInvalidToken
		}
	}
	for _, name := range []string{"iat", "nbf", "exp"} {
		var value int64
		if bytes.Equal(fields[name], []byte("null")) || json.Unmarshal(fields[name], &value) != nil || value <= 0 {
			return ErrInvalidToken
		}
	}
	var aud, permissions []string
	if json.Unmarshal(fields["aud"], &aud) != nil || len(aud) != 1 || aud[0] != FulfillmentMachineAudience {
		return ErrInvalidToken
	}
	if json.Unmarshal(fields["permissions"], &permissions) != nil || permissions == nil {
		return ErrInvalidToken
	}
	type plain FulfillmentMachineClaims
	return json.Unmarshal(data, (*plain)(c))
}
func (c FulfillmentMachineClaims) Validate() error {
	id, err := uuid.Parse(c.ClientID)
	if err != nil || id == uuid.Nil || id.String() != c.ClientID || c.Subject != "machine:"+c.ClientID || c.Kind != "machine" {
		return ErrInvalidToken
	}
	jti, err := uuid.Parse(c.ID)
	if err != nil || jti == uuid.Nil || jti.String() != c.ID {
		return ErrInvalidToken
	}
	if c.IssuedAt == nil || c.NotBefore == nil || c.ExpiresAt == nil {
		return ErrInvalidToken
	}
	if !c.NotBefore.Equal(c.IssuedAt.Time) || !c.ExpiresAt.After(c.IssuedAt.Time) || c.ExpiresAt.Sub(c.IssuedAt.Time) > FulfillmentMachineMaxLifetime {
		return ErrInvalidToken
	}
	if len(c.Permissions) < 1 || len(c.Permissions) > 3 {
		return ErrInvalidToken
	}
	previous := ""
	for _, p := range c.Permissions {
		if !fulfillmentPermissions[p] || p <= previous {
			return ErrInvalidToken
		}
		previous = p
	}
	return nil
}

type FulfillmentMachineVerifier struct {
	key    *rsa.PublicKey
	issuer string
}

func LoadFulfillmentMachine(path, issuer string) (*FulfillmentMachineVerifier, error) {
	v, err := Load(path, issuer, FulfillmentMachineAudience)
	if err != nil {
		return nil, err
	}
	return &FulfillmentMachineVerifier{v.key, v.issuer}, nil
}
func (v *FulfillmentMachineVerifier) Verify(raw string) (*FulfillmentMachineClaims, error) {
	if v == nil || v.key == nil || len(raw) > 8192 {
		return nil, ErrInvalidToken
	}
	token, err := jwt.ParseWithClaims(raw, &FulfillmentMachineClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodRS256 {
			return nil, ErrInvalidToken
		}
		return v.key, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(v.issuer), jwt.WithAudience(FulfillmentMachineAudience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(FulfillmentMachineClockTolerance))
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	c, ok := token.Claims.(*FulfillmentMachineClaims)
	if !ok {
		return nil, ErrInvalidToken
	}
	return c, nil
}

const fulfillmentIdentityKey = "inventory.verifiedFulfillmentMachine"

func ProtectFulfillments(v *FulfillmentMachineVerifier, permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		headers := c.Request.Header.Values("Authorization")
		if len(headers) != 1 {
			logger.Respond(c, 401, "unauthorized", "invalid or missing machine bearer token")
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			logger.Respond(c, 401, "unauthorized", "invalid or missing machine bearer token")
			return
		}
		claims, err := v.Verify(parts[1])
		if err != nil {
			logger.Respond(c, 401, "unauthorized", "invalid or missing machine bearer token")
			return
		}
		access := fulfillmentAccess{claims}
		if access.Authorize(c.Request.Context(), permission) != nil {
			logger.Respond(c, 403, "forbidden", "permission denied")
			return
		}
		c.Set(fulfillmentIdentityKey, access)
		c.Next()
	}
}

type fulfillmentAccess struct{ claims *FulfillmentMachineClaims }

func (a fulfillmentAccess) Actor() f.Actor {
	if a.claims == nil {
		return f.Actor{}
	}
	id, _ := uuid.Parse(a.claims.ClientID)
	return f.Actor{Kind: "MACHINE", ID: id}
}
func (a fulfillmentAccess) Authorize(ctx context.Context, permission string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := a.claims
	if c == nil || c.Validate() != nil || !fulfillmentPermissions[permission] {
		return stock.ErrForbidden
	}
	now := time.Now()
	if !now.Before(c.ExpiresAt.Add(FulfillmentMachineClockTolerance)) || now.Add(FulfillmentMachineClockTolerance).Before(c.IssuedAt.Time) {
		return stock.ErrForbidden
	}
	for _, p := range c.Permissions {
		if p == permission {
			return nil
		}
	}
	return stock.ErrForbidden
}
func FulfillmentIdentity(c *gin.Context) (f.Access, bool) {
	v, ok := c.Get(fulfillmentIdentityKey)
	access, valid := v.(fulfillmentAccess)
	return access, ok && valid && access.claims != nil
}

// Human dispatch/delivery uses the verified human identity, never a machine facade.
type humanFulfillmentAccess struct{ claims *Claims }

func (a humanFulfillmentAccess) Actor() f.Actor {
	if a.claims == nil {
		return f.Actor{}
	}
	return f.Actor{Kind: "HUMAN", ID: a.claims.UserID}
}
func (a humanFulfillmentAccess) Authorize(ctx context.Context, permission string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	c := a.claims
	if c == nil || c.Validate() != nil || !time.Now().Before(c.ExpiresAt.Time) || time.Now().Before(c.NotBefore.Time) {
		return stock.ErrForbidden
	}
	if permission != f.DispatchPermission && permission != f.DeliveryPermission && permission != f.ReadPermission {
		return stock.ErrForbidden
	}
	for _, p := range c.Permissions {
		if p == permission {
			return nil
		}
	}
	return stock.ErrForbidden
}
func HumanFulfillmentIdentity(c *gin.Context) (f.Access, bool) {
	claims, ok := Identity(c)
	return humanFulfillmentAccess{claims}, ok
}
