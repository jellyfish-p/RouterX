package common

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type JWTClaims struct {
	UserID    uint   `json:"uid"`
	Role      int    `json:"role"`
	SessionID string `json:"sid"`
	Type      string `json:"typ"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
}

func SignUserJWT(userID uint, role int, sessionID string, ttl time.Duration, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("jwt secret is empty")
	}
	now := time.Now()
	claims := JWTClaims{
		UserID:    userID,
		Role:      role,
		SessionID: sessionID,
		Type:      "user",
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
	}

	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := encodedHeader + "." + encodedClaims
	signature := signHS256(signingInput, secret)
	return signingInput + "." + signature, nil
}

func ParseUserJWT(token, secret string) (*JWTClaims, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("jwt secret is empty")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid jwt format")
	}

	signingInput := parts[0] + "." + parts[1]
	expected := signHS256(signingInput, secret)
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, errors.New("invalid jwt signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims JWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	if claims.Type != "user" {
		return nil, errors.New("invalid jwt type")
	}
	if claims.UserID == 0 {
		return nil, errors.New("invalid jwt subject")
	}
	if claims.ExpiresAt <= time.Now().Unix() {
		return nil, errors.New("jwt expired")
	}
	return &claims, nil
}

func signHS256(input, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(input))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func ParsePositiveInt(value string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func BearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return token, token != ""
}

func OpenAIError(message, typ, code string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    typ,
			"code":    code,
		},
	}
}

func FormatHTTPError(status int, message string) string {
	return fmt.Sprintf("http %d: %s", status, message)
}
