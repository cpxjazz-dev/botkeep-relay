// 密码哈希：PBKDF2-HMAC-SHA256，只用 Go 标准库，不引新依赖。
// 浏览器先把密码算成 SHA-256 再发（见 love/app.js 的 hashPassword），
// 请求里不再出现明文密码；服务端再对收到的哈希做 PBKDF2。
// 存的格式：pbkdf2-sha256-client$<迭代次数>$<base64 盐>$<base64 哈希>
// 盐 16 字节随机，哈希 32 字节。
// 迭代 10 万次：OWASP 对 SHA-256 的建议是 60 万次，但 Botkeep 免费机只有 0.5 核，
// 60 万次登录要好几秒，10 万次在弱 CPU 上约 0.3 秒、强度仍够用（登录是一次性成本，
// 会话 cookie 30 天有效，不用每次都算）。旧格式的 60 万次哈希照样校验。
// 2026-11-10 之前注册的账户存的是旧格式 pbkdf2-sha256$...（输入是明文密码），
// 登录时会自动迁移到新格式（见 love.go 的登录处理）。
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	pbkdf2Iterations = 100000
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

// clientPasswordHash 是浏览器端哈希的服务端复现：SHA-256(用户名 + "\n" + 密码)，
// 返回小写 hex。登录迁移老账户时用（把用户刚输的明文密码转成浏览器会发的那份哈希）。
func clientPasswordHash(username, password string) string {
	sum := sha256.Sum256([]byte(username + "\n" + password))
	return hex.EncodeToString(sum[:])
}

// isLegacyPasswordHash 判断存库的哈希是不是旧格式（PBKDF2 的输入是明文密码）。
func isLegacyPasswordHash(stored string) bool {
	return strings.HasPrefix(stored, "pbkdf2-sha256$")
}

// hashPassword 给（浏览器发来的）密码哈希做 PBKDF2，返回可存库的新格式字符串。
func hashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐失败: %w", err)
	}
	hash := pbkdf2SHA256([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2-sha256-client$%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// verifyPassword 校验（浏览器发来的）密码哈希与存库的哈希是否匹配。
// 新旧两种格式都认：新格式 pbkdf2-sha256-client$...，旧格式 pbkdf2-sha256$...。
// 格式不对也只返回 false。
func verifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || (parts[0] != "pbkdf2-sha256-client" && parts[0] != "pbkdf2-sha256") {
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

// looksLikeClientHash 判断收到的值是不是浏览器发来的 SHA-256 哈希（64 位小写 hex）。
// 用来区分“浏览器发哈希”和“一次性迁移时发的明文密码”。
func looksLikeClientHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
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
