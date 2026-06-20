package common

import (
	"net/textproto"
	"strings"
	"sync/atomic"
)

// DefaultRequestIDHeader is the public header RouterX uses when no custom
// observability.request_id_header setting has been loaded.
const DefaultRequestIDHeader = "X-Request-Id"

var requestIDHeaderName atomic.Value

func init() {
	requestIDHeaderName.Store(DefaultRequestIDHeader)
}

// RequestIDHeaderName returns the current process-local request id header.
func RequestIDHeaderName() string {
	if value, ok := requestIDHeaderName.Load().(string); ok && value != "" {
		return value
	}
	return DefaultRequestIDHeader
}

// SetRequestIDHeaderName updates the process-local request id header and
// returns the normalized header name that was stored.
func SetRequestIDHeaderName(value string) string {
	header := NormalizeRequestIDHeaderName(value)
	requestIDHeaderName.Store(header)
	return header
}

// NormalizeRequestIDHeaderName canonicalizes a valid HTTP header name and
// falls back to DefaultRequestIDHeader for invalid input.
func NormalizeRequestIDHeaderName(value string) string {
	value = strings.TrimSpace(value)
	if !ValidHTTPHeaderName(value) {
		return DefaultRequestIDHeader
	}
	return textproto.CanonicalMIMEHeaderKey(value)
}

// ValidHTTPHeaderName reports whether value is an RFC 9110 token suitable for
// use as an HTTP header field name.
func ValidHTTPHeaderName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !isHTTPTokenChar(value[i]) {
			return false
		}
	}
	return true
}

func isHTTPTokenChar(ch byte) bool {
	switch {
	case ch >= 'a' && ch <= 'z':
		return true
	case ch >= 'A' && ch <= 'Z':
		return true
	case ch >= '0' && ch <= '9':
		return true
	}
	switch ch {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
