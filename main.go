// 中转服务端：收一条消息，按配置转发到钉钉或 QQ 官方机器人。
// 零第三方依赖，只用 Go 标准库。配置存在 config.json，可在网页上改。
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML string

const configPath = "config.json"

// QQRecipient 是 QQ 的一个接收方：一个私聊用户或一个群。
type QQRecipient struct {
	Type    string `json:"type"`    // user（私聊）或 group（群聊）
	OpenID  string `json:"openid"`  // 接收方 openid
	Name    string `json:"name"`    // 备注名（方便认出来）
	Enabled bool   `json:"enabled"` // 关掉就不发
}

// Config 是中转目标的配置。密钥字段在读取接口里不回传明文。
type Config struct {
	Target          string        `json:"target"` // dingtalk 或 qq
	DingtalkWebhook string        `json:"dingtalk_webhook"`
	DingtalkSecret  string        `json:"dingtalk_secret"`
	QQAppID         string        `json:"qq_appid"`
	QQSecret        string        `json:"qq_secret"`
	QQRecipients    []QQRecipient `json:"qq_recipients"`
	TestMessage     string        `json:"test_message"`
	// 旧的单接收方字段，只用来迁移（读到就并进 QQRecipients），不再使用。
	QQTargetType   string `json:"qq_target_type"`
	QQTargetOpenID string `json:"qq_target_openid"`
}

var (
	cfg   Config
	cfgMu sync.RWMutex
)

func defaultConfig() Config {
	return Config{
		Target:      "dingtalk",
		TestMessage: "这是一条来自中转服务的测试消息。",
	}
}

func loadConfig() {
	cfg = defaultConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("config.json 解析失败，使用默认配置: %v", err)
		return
	}
	cfg = loaded
	// 迁移：旧的单接收方字段并进接收方列表。
	if len(cfg.QQRecipients) == 0 && cfg.QQTargetOpenID != "" {
		t := cfg.QQTargetType
		if t != "user" && t != "group" {
			t = "user"
		}
		cfg.QQRecipients = []QQRecipient{{Type: t, OpenID: cfg.QQTargetOpenID, Enabled: true}}
		cfg.QQTargetType = ""
		cfg.QQTargetOpenID = ""
	}
	if cfg.TestMessage == "" {
		cfg.TestMessage = "这是一条来自中转服务的测试消息。"
	}
}

func saveConfig() error {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

// ---------- 发送器 ----------

// sendDingTalk 用钉钉自定义机器人 webhook 发文本消息。
func sendDingTalk(text string) (bool, string) {
	cfgMu.RLock()
	webhook := cfg.DingtalkWebhook
	secret := cfg.DingtalkSecret
	cfgMu.RUnlock()
	if webhook == "" {
		return false, "未配置钉钉 webhook 地址"
	}
	sendURL := webhook
	if secret != "" {
		ts := time.Now().UnixMilli()
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(fmt.Sprintf("%d\n%s", ts, secret)))
		sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		sep := "?"
		if strings.Contains(sendURL, "?") {
			sep = "&"
		}
		sendURL = fmt.Sprintf("%s%stimestamp=%d&sign=%s", sendURL, sep, ts, url.QueryEscape(sign))
	}
	body, _ := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": text},
	})
	resp, err := http.Post(sendURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return false, "请求钉钉失败: " + err.Error()
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var result struct {
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return false, fmt.Sprintf("钉钉返回无法解析 (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	if result.Errcode != 0 {
		return false, fmt.Sprintf("钉钉拒绝 (errcode=%d): %s", result.Errcode, result.Errmsg)
	}
	return true, "钉钉已发送"
}

type qqTokenResp struct {
	AccessToken string `json:"access_token"`
	// 注意：QQ 返回的 expires_in 有时是字符串 "6253"，有时是数字 7200，
	// 这里不需要它，就不解析了，避免类型不匹配导致整个 token 解析失败。
}

// getQQToken 用 appid + secret 换 access_token。
func getQQToken() (string, error) {
	cfgMu.RLock()
	appID := cfg.QQAppID
	secret := cfg.QQSecret
	cfgMu.RUnlock()
	if appID == "" || secret == "" {
		return "", fmt.Errorf("未配置 QQ appid 或 secret")
	}
	body, _ := json.Marshal(map[string]string{"appId": appID, "clientSecret": secret})
	resp, err := http.Post("https://bots.qq.com/app/getAppAccessToken", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("请求 QQ token 失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var tr qqTokenResp
	if err := json.Unmarshal(respBody, &tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("QQ token 获取失败 (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	return tr.AccessToken, nil
}

// SendResult 是一次发送给某一个接收方的结果。
type SendResult struct {
	Target string `json:"target"` // 接收方名字，如“私聊 Alice”或“钉钉”
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// sendQQOne 给一个 QQ 接收方发文本消息。
func sendQQOne(token string, r QQRecipient, text string) (bool, string) {
	var endpoint string
	label := "私聊"
	if r.Type == "group" {
		endpoint = "https://api.sgroup.qq.com/v2/groups/" + r.OpenID + "/messages"
		label = "群聊"
	} else {
		endpoint = "https://api.sgroup.qq.com/v2/users/" + r.OpenID + "/messages"
	}
	if r.Name != "" {
		label += " " + r.Name
	}
	body, _ := json.Marshal(map[string]any{"msg_type": 0, "content": text})
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return false, label + "构造请求失败: " + err.Error()
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, label + "请求失败: " + err.Error()
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, label + "已发送"
	}
	return false, fmt.Sprintf("%s被拒绝 (HTTP %d): %s", label, resp.StatusCode, string(respBody))
}

// sendQQ 给所有启用的 QQ 接收方发文本消息。
func sendQQ(text string) []SendResult {
	cfgMu.RLock()
	recipients := append([]QQRecipient(nil), cfg.QQRecipients...)
	cfgMu.RUnlock()
	var enabled []QQRecipient
	for _, r := range recipients {
		if r.Enabled && r.OpenID != "" {
			enabled = append(enabled, r)
		}
	}
	if len(enabled) == 0 {
		return []SendResult{{Target: "QQ", OK: false, Detail: "没有启用的 QQ 接收方，先在配置页添加"}}
	}
	token, err := getQQToken()
	if err != nil {
		results := make([]SendResult, 0, len(enabled))
		for _, r := range enabled {
			label := "私聊"
			if r.Type == "group" {
				label = "群聊"
			}
			if r.Name != "" {
				label += " " + r.Name
			}
			results = append(results, SendResult{Target: label, OK: false, Detail: err.Error()})
		}
		return results
	}
	results := make([]SendResult, 0, len(enabled))
	for _, r := range enabled {
		ok, detail := sendQQOne(token, r, text)
		label := "私聊"
		if r.Type == "group" {
			label = "群聊"
		}
		if r.Name != "" {
			label += " " + r.Name
		}
		results = append(results, SendResult{Target: label, OK: ok, Detail: detail})
	}
	return results
}

// relay 把文本发到当前配置的所有目标，返回每家的结果。
// ok 为 true 当且仅当所有启用的接收方都成功。
func relay(text string) (bool, []SendResult) {
	cfgMu.RLock()
	target := cfg.Target
	cfgMu.RUnlock()
	var results []SendResult
	switch target {
	case "qq":
		results = sendQQ(text)
	default:
		ok, detail := sendDingTalk(text)
		results = []SendResult{{Target: "钉钉", OK: ok, Detail: detail}}
	}
	allOK := len(results) > 0
	for _, r := range results {
		if !r.OK {
			allOK = false
		}
	}
	return allOK, results
}

// relaySummary 把每家结果拼成一句话。
func relaySummary(results []SendResult) string {
	parts := make([]string, 0, len(results))
	for _, r := range results {
		parts = append(parts, r.Detail)
	}
	return strings.Join(parts, "；")
}

// ---------- HTTP 接口 ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// maskedConfig 返回给前端的配置，密钥只给“是否已填”。
func maskedConfig() map[string]any {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	recipients := cfg.QQRecipients
	if recipients == nil {
		recipients = []QQRecipient{}
	}
	return map[string]any{
		"target":            cfg.Target,
		"dingtalk_webhook":  cfg.DingtalkWebhook,
		"dingtalk_has_secret": cfg.DingtalkSecret != "",
		"qq_appid":          cfg.QQAppID,
		"qq_has_secret":     cfg.QQSecret != "",
		"qq_recipients":     recipients,
		"test_message":      cfg.TestMessage,
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	cfgMu.RLock()
	target := cfg.Target
	cfgMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "target": target})
}

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, maskedConfig())
}

func handlePostConfig(w http.ResponseWriter, r *http.Request) {
	var input Config
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求不是合法 JSON: " + err.Error()})
		return
	}
	if input.Target != "dingtalk" && input.Target != "qq" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target 只能是 dingtalk 或 qq"})
		return
	}
	// 规范化接收方：类型只能是 user/group，openid 为空的丢掉。
	var recipients []QQRecipient
	for _, r := range input.QQRecipients {
		if strings.TrimSpace(r.OpenID) == "" {
			continue
		}
		if r.Type != "user" && r.Type != "group" {
			r.Type = "user"
		}
		recipients = append(recipients, r)
	}
	// 兼容旧的单接收方字段。
	if len(recipients) == 0 && strings.TrimSpace(input.QQTargetOpenID) != "" {
		t := input.QQTargetType
		if t != "user" && t != "group" {
			t = "user"
		}
		recipients = []QQRecipient{{Type: t, OpenID: strings.TrimSpace(input.QQTargetOpenID), Enabled: true}}
	}
	cfgMu.Lock()
	cfg.Target = input.Target
	cfg.DingtalkWebhook = input.DingtalkWebhook
	if input.DingtalkSecret != "" {
		cfg.DingtalkSecret = input.DingtalkSecret
	}
	cfg.QQAppID = input.QQAppID
	if input.QQSecret != "" {
		cfg.QQSecret = input.QQSecret
	}
	cfg.QQRecipients = recipients
	if strings.TrimSpace(input.TestMessage) != "" {
		cfg.TestMessage = input.TestMessage
	}
	cfg.QQTargetType = ""
	cfg.QQTargetOpenID = ""
	cfgMu.Unlock()
	if err := saveConfig(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存 config.json 失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": maskedConfig()})
}

func handleTest(w http.ResponseWriter, r *http.Request) {
	text := ""
	if r.Body != nil {
		var input struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		text = strings.TrimSpace(input.Text)
	}
	if text == "" {
		cfgMu.RLock()
		text = cfg.TestMessage
		cfgMu.RUnlock()
	}
	ok, results := relay(text)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "detail": relaySummary(results), "results": results})
}

func handleRelay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只接受 POST"})
		return
	}
	var input struct {
		Text  string `json:"text"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求不是合法 JSON: " + err.Error()})
		return
	}
	text := strings.TrimSpace(input.Text)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text 不能为空"})
		return
	}
	if t := strings.TrimSpace(input.Title); t != "" {
		text = t + "\n" + text
	}
	ok, results := relay(text)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "detail": relaySummary(results), "results": results})
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

func main() {
	loadConfig()
	startQQListener()
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/openids", handleOpenIDs)
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handleGetConfig(w, r)
		} else if r.Method == http.MethodPost {
			handlePostConfig(w, r)
		} else {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "不支持的方法"})
		}
	})
	mux.HandleFunc("/api/test", handleTest)
	mux.HandleFunc("/relay", handleRelay)

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = os.Getenv("PORT")
	}
	if port == "" {
		port = "8080"
	}
	log.Printf("中转服务启动，监听 0.0.0.0:%s，当前目标: %s", port, cfg.Target)
	if err := http.ListenAndServe("0.0.0.0:"+port, mux); err != nil {
		log.Fatal(err)
	}
}
