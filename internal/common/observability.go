package common

import "sync/atomic"

var structuredLogsEnabled atomic.Bool

// StructuredLogsEnabled reports whether HTTP and panic logs should be emitted as JSON lines.
func StructuredLogsEnabled() bool {
	return structuredLogsEnabled.Load()
}

// SetStructuredLogsEnabled updates the process-local structured logging switch.
func SetStructuredLogsEnabled(enabled bool) {
	structuredLogsEnabled.Store(enabled)
}
