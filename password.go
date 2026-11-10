// 密码哈希：PBKDF2-HMAC-SHA256，只用 Go 标准库，不引新依赖。
// 存的格式：pbkdf2-sha256$<迭代次数>$<base64 盐>$<base64 哈希>
// 盐 16 字节随机，迭代 60 万次（OWASP 对 SHA-256 的建议量级），哈希 32 字节。
// 明文密码只在内存里出现一次，绝不写盘、不记日志。
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	pbkdf2Iterations = 600000
	pbkdf2SaltLen    = 16
	pbkdf2KeyLen     = 32
)

// pbkdf2SHA256 是 PBKDF2-HMAC-SHA256 的标准实现（RFC 2898）。
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	var dk []byte
	for block := 1; block <= numBlocks; block++ {
		// U1 = HMAC(password, salt || INT32BE(block))
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		// U2..Uc：每轮 HMAC(password, U(i-1))，异或进 T。
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// hashPassword 给明文密码做哈希，返回可存库的字符串。
func hashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐失败: %w", err)
	}
	hash := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// verifyPassword 校验明文密码与存库的哈希是否匹配。格式不对也只返回 false。
func verifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var errBadPassword = errors.New("密码不对")

// checkPasswordLength 是注册与改密共用的长度底线。
func checkPasswordLength(password string) error {
	if len([]byte(password)) < 6 {
		return errors.New("密码至少 6 位")
	}
	if len([]byte(password)) > 128 {
		return errors.New("密码最多 128 位")
	}
	return nil
}
