package security

import (
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const identityKey = "inventory.verifiedIdentity"

func Identity(c *gin.Context) (*Claims, bool) {
	value, exists := c.Get(identityKey)
	claims, ok := value.(*Claims)
	return claims, exists && ok && claims != nil
}

// Protect denies unlisted routes. Only the two exact GET probe routes are public.
// Policy keys use Gin route templates, not untrusted URL or proxy headers.
// Explicit alternatives apply only at the route boundary; the workflow then
// enforces its exact direction permission. Other routes keep all-of semantics.
func Protect(verifier *Verifier, policy map[string][]string, alternatives ...map[string]bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet && (c.FullPath() == "/health" || c.FullPath() == "/ready") {
			c.Next()
			return
		}
		headers := c.Request.Header.Values("Authorization")
		var claims *Claims
		var err error
		if len(headers) == 1 {
			parts := strings.Fields(headers[0])
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				claims, err = verifier.Verify(parts[1])
			}
		}
		if claims == nil || err != nil {
			c.Header("WWW-Authenticate", "Bearer")
			logger.Respond(c, http.StatusUnauthorized, "unauthorized", "invalid or missing bearer token")
			return
		}
		c.Set(identityKey, claims)
		required := policy[c.Request.Method+" "+c.FullPath()]
		if len(required) == 0 {
			logger.Respond(c, http.StatusForbidden, "forbidden", "permission denied")
			return
		}
		anyOf := len(alternatives) == 1 && alternatives[0][c.Request.Method+" "+c.FullPath()]
		matched := false
		for _, permission := range required {
			found := false
			for _, grant := range claims.Permissions {
				if grant == permission {
					found = true
					break
				}
			}
			matched = matched || found
			if !found && !anyOf {
				logger.Respond(c, http.StatusForbidden, "forbidden", "permission denied")
				return
			}
		}
		if !matched {
			logger.Respond(c, http.StatusForbidden, "forbidden", "permission denied")
			return
		}
		c.Next()
	}
}
