package common

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword 使用 bcrypt 哈希密码。
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// CheckPassword 验证 bcrypt 密码哈希。
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// GenerateTokenKey 生成 sk- 格式的 API Key (sk- + 48位 hex)。
func GenerateTokenKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(b), nil
}

// GenerateRandomString 生成指定长度的随机 hex 字符串。
func GenerateRandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateRedemCode 生成充值码 (8位数字+字母混合)。
func GenerateRedemCode() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	code := make([]byte, 8)
	for i := range code {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		code[i] = charset[n.Int64()]
	}
	return string(code)
}

// SHA256Hex 返回字符串的 SHA256 hex。
func SHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// FormatQuota 格式化额度为带单位的字符串。
// 底层存储单位为 1/QuotaPerUnit，显示时转换为货币额度。
func FormatQuota(quota int64) string {
	if quota == QuotaUnlimited {
		return "无限"
	}
	return fmt.Sprintf("%.2f", float64(quota)/float64(QuotaPerUnit))
}
