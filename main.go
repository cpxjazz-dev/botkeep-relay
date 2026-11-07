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
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
	Target           string        `json:"target"` // dingtalk 或 qq
	DingtalkWebhook  string        `json:"dingtalk_webhook"`
	DingtalkSecret   string        `json:"dingtalk_secret"`
	QQAppID          string        `json:"qq_appid"`
	QQSecret         string        `json:"qq_secret"`
	QQRecipients     []QQRecipient `json:"qq_recipients"`
	TestMessage      string        `json:"test_message"`
	APIKey           string        `json:"api_key"` // /relay 和 /api/test 的调用密钥，空着表示不设防
	HeartbeatEnabled bool          `json:"heartbeat_enabled"` // 每隔一段时间给接收方发运行状态
	HeartbeatMinutes int           `json:"heartbeat_minutes"` // 间隔（分钟），默认 60
	// 旧的单接收方字段，只用来迁移（读到就并进 QQRecipients），不再使用。
	QQTargetType   string `json:"qq_target_type"`
	QQTargetOpenID string `json:"qq_target_openid"`
}

// SendRecord 是一次发送的记录。
type SendRecord struct {
	Time    time.Time    `json:"time"`
	Source  string       `json:"source"` // relay（外部调用）或 test（页面测试）
	Text    string       `json:"text"`   // 正文（截断到 200 字）
	Results []SendResult `json:"results"`
}

const historyPath = "history.json"

var (
	history   []SendRecord
	historyMu sync.Mutex
)

var (
	cfg   Config
	cfgMu sync.RWMutex
)

func defaultConfig() Config {
	return Config{
		Target:           "dingtalk",
		TestMessage:      "这是一条来自中转服务的测试消息。",
		HeartbeatEnabled: true,
		HeartbeatMinutes: 30,
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
	// 心跳默认值：旧配置文件里没有这两个字段（分钟为 0），
	// 就按默认开启、每 30 分钟一次；新配置按用户设的值来。
	if cfg.HeartbeatMinutes <= 0 {
		cfg.HeartbeatEnabled = true
		cfg.HeartbeatMinutes = 30
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

// ---------- 发送记录 ----------

func loadHistory() {
	data, err := os.ReadFile(historyPath)
	if err != nil {
		return
	}
	var list []SendRecord
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	historyMu.Lock()
	history = list
	historyMu.Unlock()
}

func saveHistory() {
	historyMu.Lock()
	list := append([]SendRecord(nil), history...)
	historyMu.Unlock()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(historyPath, data, 0644)
}

// recordSend 记一次发送，最多留最近 50 条。
func recordSend(source, text string, results []SendResult) {
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	rec := SendRecord{
		Time:    time.Now(),
		Source:  source,
		Text:    text,
		Results: results,
	}
	historyMu.Lock()
	history = append([]SendRecord{rec}, history...)
	if len(history) > 50 {
		history = history[:50]
	}
	historyMu.Unlock()
	saveHistory()
}

// ---------- /relay 鉴权 ----------

// checkAPIKey 校验调用密钥。没设密钥时不设防；设了就必须带对。
// 密钥放请求头 X-Api-Key，或 ?key= 参数。
func checkAPIKey(r *http.Request) bool {
	cfgMu.RLock()
	key := cfg.APIKey
	cfgMu.RUnlock()
	if key == "" {
		return true
	}
	got := r.Header.Get("X-Api-Key")
	if got == "" {
		got = r.URL.Query().Get("key")
	}
	return got == key
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

// ---------- 每小时运行状态推送 ----------

var serverStartTime = time.Now()

// formatUptime 把运行时长格式化成“2小时15分”这种。
func formatUptime(d time.Duration) string {
	totalMinutes := int(d.Minutes())
	if totalMinutes < 60 {
		return fmt.Sprintf("%d分钟", totalMinutes)
	}
	hours := totalMinutes / 60
	minutes := totalMinutes % 60
	if hours < 24 {
		return fmt.Sprintf("%d小时%d分钟", hours, minutes)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%d天%d小时%d分钟", days, hours, minutes)
}

// dirSize 算一个目录下所有文件的总大小（字节）。
func dirSize(root string) int64 {
	var size int64
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size
}

// sendHeartbeat 给所有启用的接收方发一条运行状态。
func sendHeartbeat() {
	cfgMu.RLock()
	target := cfg.Target
	recipients := append([]QQRecipient(nil), cfg.QQRecipients...)
	cfgMu.RUnlock()

	var targetDesc string
	if target == "qq" {
		n := 0
		for _, r := range recipients {
			if r.Enabled && r.OpenID != "" {
				n++
			}
		}
		targetDesc = fmt.Sprintf("QQ，已启用 %d 个接收方", n)
	} else {
		targetDesc = "钉钉"
	}
	// 进程内存：Go 从系统拿到的总内存（接近容器里看到的占用）。
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	memMB := float64(m.Sys) / 1024 / 1024
	// 存储：服务所在目录下所有文件加起来。
	storageMB := float64(dirSize(".")) / 1024 / 1024
	text := fmt.Sprintf("【中转服务·运行状态】\n时间：%s\n已运行：%s\n内存：%.1f MB\n存储已用：%.1f MB\n目标：%s",
		time.Now().Format("2006-01-02 15:04:05"),
		formatUptime(time.Since(serverStartTime)),
		memMB,
		storageMB,
		targetDesc,
	)
	ok, results := relay(text)
	log.Printf("心跳已发（%s）：%s", map[bool]string{true: "全部成功", false: "有失败"}[ok], relaySummary(results))
	log.Printf("心跳内容：%s", strings.ReplaceAll(text, "\n", " | "))
}

// startHeartbeat 在后台跑：每隔配置的分钟数发一次运行状态。
// 每 10 秒重读一次配置，所以改间隔、开关都不用重启；
// 改短间隔后，下一次心跳会按新间隔尽快发出。
func startHeartbeat() {
	go func() {
		lastHeartbeat := serverStartTime
		for {
			time.Sleep(10 * time.Second)
			cfgMu.RLock()
			enabled := cfg.HeartbeatEnabled
			minutes := cfg.HeartbeatMinutes
			cfgMu.RUnlock()
			if !enabled || minutes <= 0 {
				// 没开就继续等，用户随时可能在页面上打开。
				continue
			}
			if time.Since(lastHeartbeat) >= time.Duration(minutes)*time.Minute {
				sendHeartbeat()
				lastHeartbeat = time.Now()
			}
		}
	}()
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
		"target":              cfg.Target,
		"dingtalk_webhook":    cfg.DingtalkWebhook,
		"dingtalk_has_secret": cfg.DingtalkSecret != "",
		"qq_appid":            cfg.QQAppID,
		"qq_has_secret":       cfg.QQSecret != "",
		"qq_recipients":       recipients,
		"test_message":        cfg.TestMessage,
		"has_api_key":         cfg.APIKey != "",
		"heartbeat_enabled":   cfg.HeartbeatEnabled,
		"heartbeat_minutes":   cfg.HeartbeatMinutes,
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
	// 用局部结构接请求，比 Config 多一个 api_key_clear（清除密钥用）。
	var input struct {
		Config
		APIKeyClear bool `json:"api_key_clear"`
	}
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
	// api_key 留空 = 保持原来的值；想取消鉴权就发 api_key_clear=true。
	if input.APIKeyClear {
		cfg.APIKey = ""
	} else if input.APIKey != "" {
		cfg.APIKey = input.APIKey
	}
	cfg.HeartbeatEnabled = input.HeartbeatEnabled
	if input.HeartbeatMinutes > 0 {
		cfg.HeartbeatMinutes = input.HeartbeatMinutes
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
	if !checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密钥不对，带上正确的 X-Api-Key 请求头或 ?key= 参数"})
		return
	}
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
	recordSend("test", text, results)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "detail": relaySummary(results), "results": results})
}

func handleHistory(w http.ResponseWriter, r *http.Request) {
	historyMu.Lock()
	list := append([]SendRecord(nil), history...)
	historyMu.Unlock()
	if list == nil {
		list = []SendRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": list})
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
	if !checkAPIKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密钥不对，带上正确的 X-Api-Key 请求头或 ?key= 参数"})
		return
	}
	ok, results := relay(text)
	recordSend("relay", text, results)
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
	loadHistory()
	startQQListener()
	startHeartbeat()
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/openids", handleOpenIDs)
	mux.HandleFunc("/api/history", handleHistory)
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
