// QQ 机器人网关监听：连上 QQ 的 WebSocket 网关，等有人给机器人发消息，
// 就把对方的 openid 抓下来，显示在配置页上。
//
// 为什么需要这个：QQ 不提供“QQ 号查 openid”的接口，openid 只在机器人
// 收到消息的事件里出现。所以要先让机器人收到一条消息，才能拿到 openid。
// 用法：先在配置页填好 AppID 和 AppSecret 并保存，然后用 QQ 给机器人
// 发一条消息（私聊，或把它拉进群里发），openid 就会出现在这里。
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	qqGatewayURL = "wss://api.sgroup.qq.com/websocket"
	// 群聊 + C2C 消息事件。只订阅这一个，最小权限；
	// 多要权限会导致 Identify 被拒（4014）。
	qqIntentGroupC2C = 1 << 25
	openidsPath      = "openids.json"
)

// CapturedOpenID 是抓到的一个接收方。
type CapturedOpenID struct {
	Kind         string    `json:"kind"`          // user（私聊）或 group（群聊）
	OpenID       string    `json:"openid"`        // 填到“接收方 OpenID”里的值
	Name         string    `json:"name"`          // 用户名或群名
	SenderOpenID string    `json:"sender_openid"` // 群聊时，发消息那个人的 openid
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

var (
	openids   = map[string]*CapturedOpenID{}
	openidsMu sync.RWMutex
	// 监听器状态，给配置页看。
	qqListenerState = "未启动"
	qqListenerMu    sync.RWMutex
)

func setQQListenerState(s string) {
	qqListenerMu.Lock()
	qqListenerState = s
	qqListenerMu.Unlock()
}

func getQQListenerState() string {
	qqListenerMu.RLock()
	defer qqListenerMu.RUnlock()
	return qqListenerState
}

func loadOpenIDs() {
	data, err := os.ReadFile(openidsPath)
	if err != nil {
		return
	}
	var list []CapturedOpenID
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	openidsMu.Lock()
	for i := range list {
		openids[list[i].Kind+":"+list[i].OpenID] = &list[i]
	}
	openidsMu.Unlock()
}

func saveOpenIDs() {
	openidsMu.RLock()
	list := make([]CapturedOpenID, 0, len(openids))
	for _, v := range openids {
		list = append(list, *v)
	}
	openidsMu.RUnlock()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(openidsPath, data, 0644)
}

func recordOpenID(kind, openid, name, sender string) {
	if openid == "" {
		return
	}
	key := kind + ":" + openid
	now := time.Now()
	openidsMu.Lock()
	if existing, ok := openids[key]; ok {
		existing.LastSeen = now
		if name != "" {
			existing.Name = name
		}
		if sender != "" {
			existing.SenderOpenID = sender
		}
	} else {
		openids[key] = &CapturedOpenID{
			Kind: kind, OpenID: openid, Name: name,
			SenderOpenID: sender, FirstSeen: now, LastSeen: now,
		}
		log.Printf("抓到 openid：%s %s（%s）", kind, openid, name)
	}
	openidsMu.Unlock()
	saveOpenIDs()
}

// ---------- 极简 WebSocket 客户端（只用标准库） ----------

type wsConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

// tlsClient 把一条已建立的 TCP 连接包成 TLS 连接。
func tlsClient(conn net.Conn, serverName string) *tls.Conn {
	return tls.Client(conn, &tls.Config{ServerName: serverName})
}

func wsDial(rawurl string) (*wsConn, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	var conn net.Conn
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if u.Scheme == "wss" {
		conn, err = dialer.Dial("tcp", host)
		if err != nil {
			return nil, err
		}
		// 用标准库的 TLS 握手。
		tlsConn := tlsClient(conn, u.Hostname())
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tlsConn
	} else {
		conn, err = dialer.Dial("tcp", host)
		if err != nil {
			return nil, err
		}
	}

	// Sec-WebSocket-Key：16 字节随机数 base64。
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, u.Host, key)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	// 读响应头，等 101。
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(statusLine, "101") {
		conn.Close()
		return nil, fmt.Errorf("WebSocket 握手失败：%s", strings.TrimSpace(statusLine))
	}
	// 校验 Sec-WebSocket-Accept。
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	expectAccept := base64.StdEncoding.EncodeToString(h.Sum(nil))
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "sec-websocket-accept:") {
			got := strings.TrimSpace(line[len("sec-websocket-accept:"):])
			if got != expectAccept {
				conn.Close()
				return nil, fmt.Errorf("WebSocket 握手校验失败")
			}
		}
	}
	return &wsConn{conn: conn, reader: reader}, nil
}

// readMessage 读一条完整消息，处理分片、ping（自动回 pong）、close。
func (w *wsConn) readMessage() ([]byte, error) {
	var message []byte
	for {
		// 读帧头（至少 2 字节）。
		header := make([]byte, 2)
		if _, err := io.ReadFull(w.reader, header); err != nil {
			return nil, err
		}
		fin := header[0]&0x80 != 0
		opcode := header[0] & 0x0f
		masked := header[1]&0x80 != 0
		payloadLen := int64(header[1] & 0x7f)
		switch payloadLen {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(w.reader, ext); err != nil {
				return nil, err
			}
			payloadLen = int64(binary.BigEndian.Uint16(ext))
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(w.reader, ext); err != nil {
				return nil, err
			}
			payloadLen = int64(binary.BigEndian.Uint64(ext))
		}
		var maskKey []byte
		if masked {
			maskKey = make([]byte, 4)
			if _, err := io.ReadFull(w.reader, maskKey); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(w.reader, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}
		switch opcode {
		case 0x8: // close
			return nil, io.EOF
		case 0x9: // ping -> pong
			w.writeFrame(0xA, payload)
			continue
		case 0xA: // pong
			continue
		case 0x0: // continuation
			message = append(message, payload...)
			if fin {
				return message, nil
			}
		case 0x1, 0x2: // text / binary
			message = append(message, payload...)
			if fin {
				return message, nil
			}
		default:
			// 未知操作码，忽略这一帧。
			continue
		}
	}
}

// writeFrame 写一帧（客户端发出的帧必须掩码）。
func (w *wsConn) writeFrame(opcode byte, payload []byte) error {
	maskKey := make([]byte, 4)
	if _, err := rand.Read(maskKey); err != nil {
		return err
	}
	var header []byte
	header = append(header, 0x80|opcode)
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n < 65536:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = append(header, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, ext[:]...)
	}
	header = append(header, maskKey...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ maskKey[i%4]
	}
	if _, err := w.conn.Write(header); err != nil {
		return err
	}
	_, err := w.conn.Write(masked)
	return err
}

func (w *wsConn) writeText(data []byte) error {
	return w.writeFrame(0x1, data)
}

func (w *wsConn) close() {
	w.conn.Close()
}

// ---------- QQ 网关协议 ----------

type qqGatewayMsg struct {
	Op int             `json:"op"`
	S  *int            `json:"s"`
	T  string          `json:"t"`
	D  json.RawMessage `json:"d"`
}

// startQQListener 在后台跑：连网关、等消息、抓 openid。
// 配置里没填 QQ 凭据时就等着，用户填好保存后自动开始。
func startQQListener() {
	loadOpenIDs()
	go func() {
		backoff := 5 * time.Second
		for {
			if !qqListenerOnce() {
				// 没凭据：等一会儿再看。
				setQQListenerState("等待配置 QQ AppID 和 AppSecret")
				time.Sleep(10 * time.Second)
				continue
			}
			// 连接断了：退避重连（QQ 约每 30 分钟会主动断开一次，是正常的）。
			if backoff < 60*time.Second {
				backoff *= 2
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			}
			setQQListenerState(fmt.Sprintf("连接断开，%d 秒后重连", int(backoff.Seconds())))
			time.Sleep(backoff)
		}
	}()
}

// qqListenerOnce 跑一次连接，返回 false 表示没凭据（不用重连逻辑，
// 等配置），返回 true 表示跑过一轮（无论成功还是断开）。
func qqListenerOnce() bool {
	cfgMu.RLock()
	appID := cfg.QQAppID
	secret := cfg.QQSecret
	cfgMu.RUnlock()
	if appID == "" || secret == "" {
		return false
	}
	token, err := getQQToken()
	if err != nil {
		setQQListenerState("拿 QQ token 失败：" + err.Error())
		log.Printf("QQ 监听：拿 token 失败：%v", err)
		return true
	}
	ws, err := wsDial(qqGatewayURL)
	if err != nil {
		setQQListenerState("连不上 QQ 网关：" + err.Error())
		log.Printf("QQ 监听：拨号失败：%v", err)
		return true
	}
	defer ws.close()
	setQQListenerState("已连接，等 Hello…")

	// 等 Hello（op 10），拿心跳间隔。
	var heartbeatInterval time.Duration = 30 * time.Second
	var seq *int
	helloDeadline := time.Now().Add(15 * time.Second)
	ws.conn.SetReadDeadline(helloDeadline)
	identified := false
	for {
		msg, err := ws.readMessage()
		if err != nil {
			setQQListenerState("等 Hello 失败：" + err.Error())
			return true
		}
		var gm qqGatewayMsg
		if err := json.Unmarshal(msg, &gm); err != nil {
			continue
		}
		if gm.Op == 10 {
			var hello struct {
				HeartbeatInterval int `json:"heartbeat_interval"`
			}
			json.Unmarshal(gm.D, &hello)
			if hello.HeartbeatInterval > 0 {
				heartbeatInterval = time.Duration(hello.HeartbeatInterval) * time.Millisecond
			}
			break
		}
	}
	// 发 Identify（op 2）。只订阅群聊 + C2C。
	identify := map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":   "QQBot " + token,
			"intents": qqIntentGroupC2C,
			"shard":   []int{0, 1},
			"properties": map[string]string{
				"$os":      "linux",
				"$browser": "relay",
				"$device":  "relay",
			},
		},
	}
	identifyBytes, _ := json.Marshal(identify)
	if err := ws.writeText(identifyBytes); err != nil {
		setQQListenerState("发 Identify 失败：" + err.Error())
		return true
	}
	identified = true
	_ = identified
	setQQListenerState("已连接，监听中")

	// 心跳。
	stopHeartbeat := make(chan struct{})
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// op 1 心跳，d 是最后收到的 seq（没有就 null）。
				var hb []byte
				if seq != nil {
					hb, _ = json.Marshal(map[string]any{"op": 1, "d": *seq})
				} else {
					hb, _ = json.Marshal(map[string]any{"op": 1, "d": nil})
				}
				if err := ws.writeText(hb); err != nil {
					return
				}
			case <-stopHeartbeat:
				return
			}
		}
	}()
	defer close(stopHeartbeat)

	// 读事件。
	for {
		ws.conn.SetReadDeadline(time.Now().Add(heartbeatInterval + 30*time.Second))
		msg, err := ws.readMessage()
		if err != nil {
			setQQListenerState("连接断开：" + err.Error())
			return true
		}
		var gm qqGatewayMsg
		if err := json.Unmarshal(msg, &gm); err != nil {
			continue
		}
		if gm.S != nil {
			seq = gm.S
		}
		switch gm.Op {
		case 0: // Dispatch
			handleQQDispatch(gm.T, gm.D)
		case 7: // Reconnect：服务端让重连。
			setQQListenerState("服务端要求重连")
			return true
		case 9: // Invalid Session：token 或 intents 有问题，重连。
			setQQListenerState("会话失效（可能 intents 没开通），重连")
			return true
		case 11: // Heartbeat ACK
		}
	}
}

// handleQQDispatch 处理一条网关事件，抓 openid。
func handleQQDispatch(eventType string, d json.RawMessage) {
	switch eventType {
	case "C2C_MESSAGE_CREATE":
		var ev struct {
			Author struct {
				UserOpenID string `json:"user_openid"`
				ID         string `json:"id"`
				Username   string `json:"username"`
			} `json:"author"`
		}
		if err := json.Unmarshal(d, &ev); err != nil {
			return
		}
		openid := ev.Author.UserOpenID
		if openid == "" {
			openid = ev.Author.ID
		}
		recordOpenID("user", openid, ev.Author.Username, "")
	case "GROUP_AT_MESSAGE_CREATE", "GROUP_MESSAGE_CREATE":
		var ev struct {
			GroupOpenID string `json:"group_openid"`
			GroupName   string `json:"group_name"`
			Author      struct {
				UserOpenID   string `json:"user_openid"`
				MemberOpenID string `json:"member_openid"`
				ID           string `json:"id"`
				Username     string `json:"username"`
			} `json:"author"`
		}
		if err := json.Unmarshal(d, &ev); err != nil {
			return
		}
		sender := ev.Author.UserOpenID
		if sender == "" {
			sender = ev.Author.MemberOpenID
		}
		if sender == "" {
			sender = ev.Author.ID
		}
		recordOpenID("group", ev.GroupOpenID, ev.GroupName, sender)
	case "READY":
		setQQListenerState("已连接，监听中")
	}
}

func handleOpenIDs(w http.ResponseWriter, r *http.Request) {
	openidsMu.RLock()
	list := make([]CapturedOpenID, 0, len(openids))
	for _, v := range openids {
		list = append(list, *v)
	}
	openidsMu.RUnlock()
	// 按最近出现排前面。
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].LastSeen.After(list[i].LastSeen) {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state":   getQQListenerState(),
		"openids": list,
	})
}
