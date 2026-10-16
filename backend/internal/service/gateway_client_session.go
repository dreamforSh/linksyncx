package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/gin-gonic/gin"
)

// MessagesClientSessionIDFromHeader resolves a sanitized session identifier for
// Messages sticky routing, preserving the recognized header order in session_id.go.
// It ignores proxy headers, request IDs, and protocol-specific Grok headers. The
// result is local routing input only and must not be forwarded as upstream identity.
func MessagesClientSessionIDFromHeader(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	for _, header := range clientSessionIDHeaders {
		if sessionID := sanitizeSessionID(c.GetHeader(header)); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func clientSessionHash(ctx *SessionContext) string {
	if ctx == nil || ctx.APIKeyID <= 0 {
		return ""
	}
	sessionID := sanitizeSessionID(ctx.ClientSessionID)
	if sessionID == "" {
		return ""
	}

	// A fixed namespace and the delimited numeric key ID make the seed
	// unambiguous. Keep IP, UA, and growing history out of explicit session
	// identity; SHA-256 and a distinct output prefix separate it from the legacy
	// short content hashes without exposing the raw client identifier.
	seed := "messages-client-session:v1:" + strconv.FormatInt(ctx.APIKeyID, 10) + ":" + sessionID
	sum := sha256.Sum256([]byte(seed))
	return "cs1_" + hex.EncodeToString(sum[:])
}
