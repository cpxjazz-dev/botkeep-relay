// love 站账户与照片接口：注册、登录、会话、改密、展示效果偏好、上传、照片列表、管理员看用户。
// 用户数据进 Neon Postgres，照片文件进 Neon Object Storage（S3），密码只存 PBKDF2 哈希。
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	loveSessionCookie = "love_session"
	loveSessionTTL    = 30 * 24 * time.Hour
	loveMaxUpload     = 5 << 20 // 5 MB
	loveMaxPhotosPerUser = 50   // 每人最多 50 张，防止一个人把对象存储撑爆
)

// LoveUser 是 love 站的一个账户。
type LoveUser struct {
	ID            int    `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	IsSuperadmin  bool   `json:"is_superadmin"`
	DisplayEffect string `json:"display_effect"`
	CreatedAt     string `json:"created_at"`
}

// ---------- 工具 ----------

func loveDBReady(w http.ResponseWriter) bool {
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "数据库未连接，账户功能暂不可用"})
		return false
	}
	if s3cfg == nil {
		// 账户功能不需要 S3，只在上传时检查；这里不拦截。
	}
	return true
}

func superadminUsername() string {
	if v := os.Getenv("LOVE_SUPERADMIN_USERNAME"); v != "" {
		return v
	}
	return "cpxjazz"
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,20}$`)

func validUsername(u string) bool { return usernameRe.MatchString(u) }

func validDisplayEffect(e string) bool {
	switch e {
	case "polaroid", "grid", "carousel", "masonry", "coverflow", "justified", "stack", "scatter", "tree", "ring":
		return true
	}
	return false
}

// ---------- 会话 ----------

func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashSessionToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func createSession(userID int) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}
	_, err = db.Exec(
		`INSERT INTO love_sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashSessionToken(token), userID, time.Now().Add(loveSessionTTL),
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     loveSessionCookie,
		Value:    token,
		Path:     "/love/",
		MaxAge:   int(loveSessionTTL / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     loveSessionCookie,
		Value:    "",
		Path:     "/love/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// getSessionUser 从 cookie 里拿当前登录用户。没登录、过期或库里没有都返回 nil。
func getSessionUser(r *http.Request) *LoveUser {
	if db == nil {
		return nil
	}
	c, err := r.Cookie(loveSessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	var u LoveUser
	err = db.QueryRow(
		`SELECT u.id, u.username, u.display_name, u.is_superadmin, u.display_effect, u.created_at
		 FROM love_users u JOIN love_sessions s ON s.user_id = u.id
		 WHERE s.token_hash = $1 AND s.expires_at > now()`,
		hashSessionToken(c.Value),
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsSuperadmin, &u.DisplayEffect, &u.CreatedAt)
	if err != nil {
		return nil
	}
	return &u
}

func deleteSession(r *http.Request) {
	if db == nil {
		return
	}
	c, err := r.Cookie(loveSessionCookie)
	if err != nil || c.Value == "" {
		return
	}
	db.Exec(`DELETE FROM love_sessions WHERE token_hash = $1`, hashSessionToken(c.Value))
}

// ensureSuperadmin：用户名与 LOVE_SUPERADMIN_USERNAME 一致的账户，
// 注册或登录时自动升为 superadmin（只升这一个指定账户）。
func ensureSuperadmin(username string) {
	if db == nil || username != superadminUsername() {
		return
	}
	db.Exec(`UPDATE love_users SET is_superadmin = TRUE WHERE username = $1`, username)
}

// ---------- 注册 / 登录 / 登出 / 改密 ----------

func handleLoveRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只接受 POST"})
		return
	}
	if !loveDBReady(w) {
		return
	}
	var input struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求不是合法 JSON"})
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if !validUsername(input.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "用户名 3–20 位，只能是字母、数字、下划线"})
		return
	}
	if err := checkPasswordLength(input.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if input.DisplayName == "" {
		input.DisplayName = input.Username
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "密码处理失败"})
		return
	}
	var userID int
	err = db.QueryRow(
		`INSERT INTO love_users (username, display_name, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		input.Username, input.DisplayName, hash,
	).Scan(&userID)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "love_users_username_key") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "这个用户名已被注册"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "注册失败"})
		return
	}
	// 指定的 superadmin 账户注册即升为 superadmin。
	ensureSuperadmin(input.Username)
	token, err := createSession(userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "创建会话失败"})
		return
	}
	setSessionCookie(w, token)
	u := getUserByID(userID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
}

func getUserByID(id int) *LoveUser {
	var u LoveUser
	err := db.QueryRow(
		`SELECT id, username, display_name, is_superadmin, display_effect, created_at FROM love_users WHERE id = $1`,
		id,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsSuperadmin, &u.DisplayEffect, &u.CreatedAt)
	if err != nil {
		return nil
	}
	return &u
}

func handleLoveLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只接受 POST"})
		return
	}
	if !loveDBReady(w) {
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求不是合法 JSON"})
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	var u LoveUser
	var hash string
	err := db.QueryRow(
		`SELECT id, username, display_name, is_superadmin, display_effect, created_at, password_hash
		 FROM love_users WHERE username = $1`,
		input.Username,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsSuperadmin, &u.DisplayEffect, &u.CreatedAt, &hash)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "用户名或密码不对"})
		return
	}
	if !verifyPassword(input.Password, hash) {
		// 老账户存的是明文密码的 PBKDF2，新版浏览器发来的是 SHA-256 哈希，
		// 对不上是正常的——告诉浏览器走一次性迁移（只发一次明文密码，见下）。
		if isLegacyPasswordHash(hash) && looksLikeClientHash(input.Password) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密码存储需要升级，请再登录一次完成升级", "code": "password_rehash_required"})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "用户名或密码不对"})
		return
	}
	// 老账户：如果这次发来的是明文密码且对上了，顺手迁到新格式
	// （把明文转成浏览器会发的那份 SHA-256 哈希，再做 PBKDF2 存起来）。
	// 以后这个账户登录就只发哈希，不再发明文。
	if isLegacyPasswordHash(hash) && !looksLikeClientHash(input.Password) {
		if newHash, err := hashPassword(clientPasswordHash(input.Username, input.Password)); err == nil {
			db.Exec(`UPDATE love_users SET password_hash = $1 WHERE id = $2`, newHash, u.ID)
		}
	}
	// 指定的 superadmin 账户登录时也确保是 superadmin。
	ensureSuperadmin(u.Username)
	token, err := createSession(u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "创建会话失败"})
		return
	}
	setSessionCookie(w, token)
	// 重新读一次，确保 is_superadmin 是最新的。
	if fresh := getUserByID(u.ID); fresh != nil {
		u = *fresh
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": &u})
}

func handleLoveLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只接受 POST"})
		return
	}
	deleteSession(r)
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleLoveMe(w http.ResponseWriter, r *http.Request) {
	if !loveDBReady(w) {
		return
	}
	u := getSessionUser(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
}

func handleLovePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只接受 POST"})
		return
	}
	if !loveDBReady(w) {
		return
	}
	u := getSessionUser(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return
	}
	var input struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求不是合法 JSON"})
		return
	}
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM love_users WHERE id = $1`, u.ID).Scan(&hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取账户失败"})
		return
	}
	if !verifyPassword(input.Current, hash) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "当前密码不对"})
		return
	}
	if err := checkPasswordLength(input.New); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	newHash, err := hashPassword(input.New)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "密码处理失败"})
		return
	}
	if _, err := db.Exec(`UPDATE love_users SET password_hash = $1 WHERE id = $2`, newHash, u.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "改密失败"})
		return
	}
	// 改密后旧会话全部作废，重新登录。
	db.Exec(`DELETE FROM love_sessions WHERE user_id = $1`, u.ID)
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 展示效果偏好 ----------

func handleLovePrefs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只接受 POST"})
		return
	}
	if !loveDBReady(w) {
		return
	}
	u := getSessionUser(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return
	}
	var input struct {
		DisplayEffect string `json:"display_effect"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求不是合法 JSON"})
		return
	}
	if !validDisplayEffect(input.DisplayEffect) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "展示效果只能是 polaroid、grid、carousel、masonry、coverflow、justified、stack、scatter、tree、ring"})
		return
	}
	if _, err := db.Exec(`UPDATE love_users SET display_effect = $1 WHERE id = $2`, input.DisplayEffect, u.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存偏好失败"})
		return
	}
	u.DisplayEffect = input.DisplayEffect
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
}

// ---------- 上传与照片 ----------

var s3cfg *s3Config

func extForContentType(ct string) string {
	switch ct {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// LovePhoto 是一张上传的照片（带一个 1 小时有效的查看链接）。
// URL 是 1600px 的原图（灯箱用），ThumbURL 是 400px 的缩略图（画廊用，加载快得多；
// 老照片没有缩略图时为空，前端用 URL 兜底）。
type LovePhoto struct {
	ID          int    `json:"id"`
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Filename    string `json:"filename"`
	Caption     string `json:"caption"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	URL         string `json:"url"`
	ThumbURL    string `json:"thumb_url,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// loveProxyURL 把 S3 对象 key 变成走服务器中转的图片地址。
// 浏览器不再直连 S3（手机到 Neon S3 的链路不稳、容易破图），改走服务器——
// 服务器到 S3 是机房到机房、又快又稳；浏览器到服务器（尤其日本节点）
// 也比直连 S3 稳。代理地址是同源的，没有过期时间，浏览器还能缓存。
func loveProxyURL(key string) string {
	return "/love/api/img?key=" + url.QueryEscape(key)
}

// handleLoveImageProxy：图片代理。登录后才能看，只能看自己的照片
// （key 必须是 u<自己的ID>/ 开头），superadmin 能看任意人的。
// 从 S3 拉出来原样返回，带一天的浏览器缓存。
func handleLoveImageProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只接受 GET"})
		return
	}
	if !loveDBReady(w) {
		return
	}
	u := getSessionUser(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return
	}
	if s3cfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "对象存储未配置"})
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key 不合法"})
		return
	}
	// key 格式是 u<用户ID>/... 或 u<用户ID>/thumb/...，从里面读出归属用户。
	ownerID := -1
	if strings.HasPrefix(key, "u") {
		rest := key[1:]
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i > 0 && i < len(rest) && rest[i] == '/' {
			fmt.Sscanf(rest[:i], "%d", &ownerID)
		}
	}
	if ownerID < 0 || (ownerID != u.ID && !u.IsSuperadmin) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "只能看自己的照片"})
		return
	}
	body, ct, err := s3cfg.s3GetObject(key)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "图片不存在"})
		return
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func handleLoveUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只接受 POST"})
		return
	}
	if !loveDBReady(w) {
		return
	}
	u := getSessionUser(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return
	}
	if s3cfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "对象存储未配置，上传暂不可用"})
		return
	}
	// 每人上限：先数数，满了就拒掉，别等传到 S3 才发现。
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM love_photos WHERE user_id = $1`, u.ID).Scan(&count); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询照片数失败"})
		return
	}
	if count >= loveMaxPhotosPerUser {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("每人最多上传 %d 张照片，你的相册已满", loveMaxPhotosPerUser)})
		return
	}
	if err := r.ParseMultipartForm(loveMaxUpload + (1 << 20)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "表单解析失败: " + err.Error()})
		return
	}
	file, header, err := r.FormFile("photo")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请选一张图片上传（字段 photo）"})
		return
	}
	defer file.Close()
	if header.Size > loveMaxUpload {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "图片不能超过 5 MB"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, loveMaxUpload+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取图片失败"})
		return
	}
	if int64(len(data)) > loveMaxUpload {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "图片不能超过 5 MB"})
		return
	}
	ct := http.DetectContentType(data)
	ext := extForContentType(ct)
	if ext == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "只支持 JPG、PNG、WebP、GIF 图片"})
		return
	}
	// 先压再存：最长边 1600px、JPEG 质量 82，加载快一个量级。
	// （GIF 动图和解不出来的原样存。）
	filename := header.Filename
	data, filename, ct = compressLoveImage(data, filename)
	ext = extForContentType(ct)
	caption := strings.TrimSpace(r.FormValue("caption"))
	if len(caption) > 200 {
		caption = caption[:200]
	}
	key := fmt.Sprintf("u%d/%s-%s.%s", u.ID, time.Now().Format("20060102"), randomHex(6), ext)
	if err := s3cfg.s3PutObject(key, ct, data); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "存入对象存储失败: " + err.Error()})
		return
	}
	// 缩略图：画廊只加载 400px 的小图，灯箱才用原图。缩略图失败不影响主流程，
	// 前端没有 thumb_url 时用原图兜底。
	var thumbKey string
	if thumb := makeLoveThumbnail(data); thumb != nil {
		thumbKey = fmt.Sprintf("u%d/thumb/%s-%s.jpg", u.ID, time.Now().Format("20060102"), randomHex(6))
		if err := s3cfg.s3PutObject(thumbKey, "image/jpeg", thumb); err != nil {
			thumbKey = ""
		}
	}
	var photoID int
	err = db.QueryRow(
		`INSERT INTO love_photos (user_id, object_key, thumb_object_key, filename, caption, content_type, size_bytes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		u.ID, key, sql.NullString{String: thumbKey, Valid: thumbKey != ""}, filename, caption, ct, len(data),
	).Scan(&photoID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存照片记录失败"})
		return
	}
	photo := LovePhoto{
		ID: photoID, UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
		Filename: filename, Caption: caption, ContentType: ct, SizeBytes: int64(len(data)),
	}
	// 走服务器中转，不再发预签名链接（见 loveProxyURL）。
	photo.URL = loveProxyURL(key)
	if thumbKey != "" {
		photo.ThumbURL = loveProxyURL(thumbKey)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "photo": &photo})
}

// listLovePhotos 查照片并给每张配一个走服务器中转的查看链接（原图 + 缩略图）。
// 中转地址没有过期时间，浏览器还能缓存，比预签名链接稳。
func listLovePhotos(userID *int, limit int) ([]LovePhoto, error) {
	var rows *sql.Rows
	var err error
	if userID != nil {
		rows, err = db.Query(
			`SELECT p.id, p.user_id, u.username, u.display_name, p.filename, p.caption,
			        p.content_type, p.size_bytes, p.object_key, p.thumb_object_key, p.created_at
			 FROM love_photos p JOIN love_users u ON u.id = p.user_id
			 WHERE p.user_id = $1 ORDER BY p.created_at DESC LIMIT $2`, *userID, limit)
	} else {
		rows, err = db.Query(
			`SELECT p.id, p.user_id, u.username, u.display_name, p.filename, p.caption,
			        p.content_type, p.size_bytes, p.object_key, p.thumb_object_key, p.created_at
			 FROM love_photos p JOIN love_users u ON u.id = p.user_id
			 ORDER BY p.created_at DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	photos := []LovePhoto{}
	for rows.Next() {
		var p LovePhoto
		var key string
		var thumbKey sql.NullString
		if err := rows.Scan(&p.ID, &p.UserID, &p.Username, &p.DisplayName, &p.Filename,
			&p.Caption, &p.ContentType, &p.SizeBytes, &key, &thumbKey, &p.CreatedAt); err != nil {
			return nil, err
		}
		if s3cfg != nil {
			// 走服务器中转，不再发预签名链接（见 loveProxyURL）。
			p.URL = loveProxyURL(key)
			if thumbKey.Valid && thumbKey.String != "" {
				p.ThumbURL = loveProxyURL(thumbKey.String)
			}
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}

func handleLovePhotos(w http.ResponseWriter, r *http.Request) {
	if !loveDBReady(w) {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	// ?user_id= 只给 superadmin 用：想看谁的看谁的。
	if v := r.URL.Query().Get("user_id"); v != "" {
		me := getSessionUser(r)
		if me == nil || !me.IsSuperadmin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "只有 superadmin 能按用户查看照片"})
			return
		}
		uid, err := strconv.Atoi(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id 不合法"})
			return
		}
		photos, err := listLovePhotos(&uid, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询照片失败"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"photos": photos})
		return
	}
	// 默认只返回登录用户自己的照片：每个人的相册互相看不见。
	// 未登录返回 401，前端据此显示“登录后查看你的照片”。
	me := getSessionUser(r)
	if me == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return
	}
	photos, err := listLovePhotos(&me.ID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询照片失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"photos": photos})
}

// ---------- 管理员：用户列表 ----------

func handleLoveUsers(w http.ResponseWriter, r *http.Request) {
	if !loveDBReady(w) {
		return
	}
	me := getSessionUser(r)
	if me == nil || !me.IsSuperadmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "只有 superadmin 能看用户列表"})
		return
	}
	rows, err := db.Query(
		`SELECT u.id, u.username, u.display_name, u.is_superadmin, u.display_effect, u.created_at,
		        (SELECT count(*) FROM love_photos p WHERE p.user_id = u.id)
		 FROM love_users u ORDER BY u.created_at ASC`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询用户失败"})
		return
	}
	defer rows.Close()
	type userRow struct {
		ID            int    `json:"id"`
		Username      string `json:"username"`
		DisplayName   string `json:"display_name"`
		IsSuperadmin  bool   `json:"is_superadmin"`
		DisplayEffect string `json:"display_effect"`
		PhotoCount    int    `json:"photo_count"`
		CreatedAt     string `json:"created_at"`
	}
	users := []userRow{}
	for rows.Next() {
		var u userRow
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsSuperadmin,
			&u.DisplayEffect, &u.CreatedAt, &u.PhotoCount); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "查询用户失败"})
			return
		}
		users = append(users, u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}
