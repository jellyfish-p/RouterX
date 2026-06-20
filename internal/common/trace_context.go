package common

import "strings"

// TraceparentHeaderName and TracestateHeaderName are the W3C Trace Context
// headers RouterX preserves when a caller already has a distributed trace.
// RouterX still treats request_id as the human-facing lookup key.
const (
	TraceparentHeaderName = "Traceparent"
	TracestateHeaderName  = "Tracestate"
)

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

// NormalizeTracestate validates the companion W3C tracestate header. It keeps
// a conservative subset of the spec: up to 32 comma-separated key=value members
// and a 512 byte total limit, with members normalized by trimming optional
// whitespace around commas. Invalid values are ignored at trust boundaries.
func NormalizeTracestate(value string) (string, bool) {
	value = strings.Trim(value, " \t")
	if value == "" || len(value) > 512 {
		return "", false
	}
	rawMembers := strings.Split(value, ",")
	if len(rawMembers) == 0 || len(rawMembers) > 32 {
		return "", false
	}
	members := make([]string, 0, len(rawMembers))
	for _, raw := range rawMembers {
		member := strings.Trim(raw, " \t")
		idx := strings.IndexByte(member, '=')
		if idx <= 0 {
			return "", false
		}
		key, memberValue := member[:idx], member[idx+1:]
		if !validTracestateKey(key) || !validTracestateValue(memberValue) {
			return "", false
		}
		members = append(members, member)
	}
	return strings.Join(members, ","), true
}

func validTracestateKey(key string) bool {
	if key == "" || len(key) > 256 || strings.Count(key, "@") > 1 {
		return false
	}
	if strings.Contains(key, "@") {
		parts := strings.Split(key, "@")
		return validTracestateSimpleKey(parts[0]) && validTracestateSimpleKey(parts[1])
	}
	return validTracestateSimpleKey(key)
}

func validTracestateSimpleKey(key string) bool {
	if key == "" || len(key) > 256 {
		return false
	}
	first := key[0]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	for i := 1; i < len(key); i++ {
		ch := key[i]
		if (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '_' || ch == '-' || ch == '*' || ch == '/' {
			continue
		}
		return false
	}
	return true
}

func validTracestateValue(value string) bool {
	if value == "" || len(value) > 256 || value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t' {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 0x20 && ch <= 0x2b) ||
			(ch >= 0x2d && ch <= 0x3c) ||
			(ch >= 0x3e && ch <= 0x7e) {
			continue
		}
		return false
	}
	return true
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
