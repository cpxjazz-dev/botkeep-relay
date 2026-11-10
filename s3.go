// Neon Object Storage（S3 兼容）最小客户端，只用 Go 标准库。
// SigV4 签名自己算：PUT 上传 + 预签名 GET 链接。路径风格（path-style）是 Neon 强制要求的。
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type s3Config struct {
	AccessKey string
	SecretKey string
	Endpoint  string // 如 https://br-xxx.storage.c-4.ap-southeast-1.aws.neon.tech
	Region    string
	Bucket    string
}

func loadS3Config() *s3Config {
	ak := os.Getenv("AWS_ACCESS_KEY_ID")
	sk := os.Getenv("AWS_SECRET_ACCESS_KEY")
	ep := os.Getenv("AWS_ENDPOINT_URL_S3")
	region := os.Getenv("AWS_REGION")
	bucket := os.Getenv("NEON_STORAGE_BUCKET")
	if ak == "" || sk == "" || ep == "" || region == "" || bucket == "" {
		return nil
	}
	return &s3Config{
		AccessKey: ak,
		SecretKey: sk,
		Endpoint:  strings.TrimSuffix(ep, "/"),
		Region:    region,
		Bucket:    bucket,
	}
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// awsEncode 按 SigV4 要求编码：只留不保留字符（A-Z a-z 0-9 - _ . ~）不编码。
func awsEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// encodePath 每段分别编码，斜杠保留。
func encodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = awsEncode(part, true)
	}
	return strings.Join(parts, "/")
}

func (c *s3Config) signingKey(t time.Time) []byte {
	kDate := hmacSHA256([]byte("AWS4"+c.SecretKey), []byte(t.Format("20060102")))
	kRegion := hmacSHA256(kDate, []byte(c.Region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func (c *s3Config) scope(t time.Time) string {
	return fmt.Sprintf("%s/%s/s3/aws4_request", t.Format("20060102"), c.Region)
}

// s3PutObject 上传一个对象到桶里。key 如 u123/20261110-abcdef.jpg。
func (c *s3Config) s3PutObject(key, contentType string, body []byte) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	payloadHash := sha256Hex(body)
	host := strings.TrimPrefix(c.Endpoint, "https://")
	host = strings.TrimPrefix(host, "http://")
	uri := "/" + c.Bucket + "/" + encodePath(key)

	headers := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
		"content-type":         contentType,
	}
	var names []string
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + strings.TrimSpace(headers[n]) + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonReq := strings.Join([]string{
		"PUT", uri, "", canonHeaders.String(), signedHeaders, payloadHash,
	}, "\n")
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + c.scope(now) + "\n" + sha256Hex([]byte(canonReq))
	sig := hex.EncodeToString(hmacSHA256(c.signingKey(now), []byte(stringToSign)))
	auth := "AWS4-HMAC-SHA256 Credential=" + c.AccessKey + "/" + c.scope(now) +
		", SignedHeaders=" + signedHeaders + ", Signature=" + sig

	req, err := http.NewRequest(http.MethodPut, c.Endpoint+uri, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("S3 上传请求失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("S3 上传被拒 (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// s3GetObject 从桶里取一个对象，返回内容和 content-type。给图片代理接口用
// （浏览器不再直连 S3，改走服务器中转，加载更快更稳）。
func (c *s3Config) s3GetObject(key string) ([]byte, string, error) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	host := strings.TrimPrefix(c.Endpoint, "https://")
	host = strings.TrimPrefix(host, "http://")
	uri := "/" + c.Bucket + "/" + encodePath(key)
	// GET 请求体为空，payload 哈希是空串的 SHA-256。
	payloadHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	headers := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	var names []string
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + strings.TrimSpace(headers[n]) + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonReq := strings.Join([]string{
		"GET", uri, "", canonHeaders.String(), signedHeaders, payloadHash,
	}, "\n")
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + c.scope(now) + "\n" + sha256Hex([]byte(canonReq))
	sig := hex.EncodeToString(hmacSHA256(c.signingKey(now), []byte(stringToSign)))
	auth := "AWS4-HMAC-SHA256 Credential=" + c.AccessKey + "/" + c.scope(now) +
		", SignedHeaders=" + signedHeaders + ", Signature=" + sig

	req, err := http.NewRequest(http.MethodGet, c.Endpoint+uri, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("S3 拉取请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("S3 拉取被拒 (HTTP %d): %s", resp.StatusCode, string(body))
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// s3PresignGet 生成一个有时限的 GET 预签名链接，浏览器可直接打开看图。
func (c *s3Config) s3PresignGet(key string, expires time.Duration) (string, error) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	host := strings.TrimPrefix(c.Endpoint, "https://")
	host = strings.TrimPrefix(host, "http://")
	uri := "/" + c.Bucket + "/" + encodePath(key)
	expireSecs := int(expires / time.Second)
	if expireSecs <= 0 {
		expireSecs = 3600
	}
	credential := c.AccessKey + "/" + dateStamp + "/" + c.Region + "/s3/aws4_request"
	params := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    credential,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       fmt.Sprintf("%d", expireSecs),
		"X-Amz-SignedHeaders": "host",
	}
	var keys []string
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var q strings.Builder
	for i, k := range keys {
		if i > 0 {
			q.WriteByte('&')
		}
		q.WriteString(awsEncode(k, true) + "=" + awsEncode(params[k], true))
	}
	canonReq := strings.Join([]string{
		"GET", uri, q.String(), "host:" + host + "\n", "host", "UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + c.scope(now) + "\n" + sha256Hex([]byte(canonReq))
	sig := hex.EncodeToString(hmacSHA256(c.signingKey(now), []byte(stringToSign)))
	return c.Endpoint + uri + "?" + q.String() + "&X-Amz-Signature=" + sig, nil
}
