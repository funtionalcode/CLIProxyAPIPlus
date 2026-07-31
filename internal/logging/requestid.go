package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// requestIDKey is the context key for storing/retrieving request IDs.
type requestIDKey struct{}

// ginRequestIDKey is the Gin context key for request IDs.
const ginRequestIDKey = "__request_id__"

// Incoming request-id headers accepted from upstream gateways such as new-api.
// Prefer the client-facing correlation header, then the new-api instance header.
var incomingRequestIDHeaders = []string{
	"X-Client-Request-Id",
	"X-Oneapi-Request-Id",
}

const (
	// maxIncomingRequestIDLen bounds log filenames and lookup keys.
	maxIncomingRequestIDLen = 128
)

var (
	// unsafeRequestIDChars matches characters that cannot appear in a log filename segment.
	unsafeRequestIDChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	// multiHyphen collapses repeated separators produced by sanitization.
	multiHyphen = regexp.MustCompile(`-+`)
)

// GenerateRequestID creates a new 8-character hex request ID.
func GenerateRequestID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b)
}

// SanitizeRequestID normalizes an external request ID for log filenames and lookups.
// Empty or unsafe-only input returns an empty string so callers can fall back to a local ID.
func SanitizeRequestID(raw string) string {
	sanitized := strings.TrimSpace(raw)
	if sanitized == "" {
		return ""
	}
	// Keep only filename-safe characters so request-log-by-id can match the on-disk suffix.
	sanitized = unsafeRequestIDChars.ReplaceAllString(sanitized, "-")
	sanitized = multiHyphen.ReplaceAllString(sanitized, "-")
	sanitized = strings.Trim(sanitized, "-.")
	if sanitized == "" {
		return ""
	}
	if len(sanitized) > maxIncomingRequestIDLen {
		sanitized = strings.Trim(sanitized[:maxIncomingRequestIDLen], "-.")
	}
	return sanitized
}

// ResolveIncomingRequestID returns a sanitized request ID from known inbound headers.
// Returns empty string when no usable external ID is present.
func ResolveIncomingRequestID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	for _, header := range incomingRequestIDHeaders {
		if id := SanitizeRequestID(c.GetHeader(header)); id != "" {
			return id
		}
	}
	return ""
}

// WithRequestID returns a new context with the request ID attached.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// GetRequestID retrieves the request ID from the context.
// Returns empty string if not found.
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// SetGinRequestID stores the request ID in the Gin context.
func SetGinRequestID(c *gin.Context, requestID string) {
	if c != nil {
		c.Set(ginRequestIDKey, requestID)
	}
}

// GetGinRequestID retrieves the request ID from the Gin context.
func GetGinRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if id, exists := c.Get(ginRequestIDKey); exists {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}
