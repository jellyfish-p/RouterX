package common

import "strings"

// TraceparentHeaderName is the W3C Trace Context header RouterX preserves when
// a caller already has a distributed trace. RouterX still treats request_id as
// the human-facing lookup key; traceparent links the same request across hops.
const TraceparentHeaderName = "Traceparent"

// NormalizeTraceparent validates a W3C traceparent value and returns the
// canonical lower-case header plus its trace id. Invalid values are ignored so
// untrusted callers cannot poison structured logs or upstream trace context.
func NormalizeTraceparent(value string) (string, string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 55 ||
		value[2] != '-' ||
		value[35] != '-' ||
		value[52] != '-' {
		return "", "", false
	}
	version := value[0:2]
	traceID := value[3:35]
	parentID := value[36:52]
	flags := value[53:55]
	if version == "ff" ||
		!isLowerHex(version) ||
		!isLowerHex(traceID) ||
		!isLowerHex(parentID) ||
		!isLowerHex(flags) ||
		allZeroHex(traceID) ||
		allZeroHex(parentID) {
		return "", "", false
	}
	return value, traceID, true
}

func isLowerHex(value string) bool {
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return false
	}
	return true
}

func allZeroHex(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] != '0' {
			return false
		}
	}
	return true
}
