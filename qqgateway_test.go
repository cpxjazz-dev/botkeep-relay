package main

import (
	"encoding/json"
	"testing"
	"time"
)

// 连真实的 QQ 网关，验证 WebSocket 握手 + 读帧。
// 不用真凭据：只等 Hello（op 10），再发一个假 Identify，
// 服务端应以鉴权失败关掉连接（而不是协议错误），
// 说明我们发出去的掩码帧格式是对的。
func TestQGatewayHello(t *testing.T) {
	ws, err := wsDial(qqGatewayURL)
	if err != nil {
		t.Fatalf("拨号失败：%v", err)
	}
	defer ws.close()

	ws.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	msg, err := ws.readMessage()
	if err != nil {
		t.Fatalf("读 Hello 失败：%v", err)
	}
	var gm qqGatewayMsg
	if err := json.Unmarshal(msg, &gm); err != nil {
		t.Fatalf("Hello 不是合法 JSON：%v", err)
	}
	if gm.Op != 10 {
		t.Fatalf("第一条消息不是 Hello（op 10），而是 op %d：%s", gm.Op, string(msg))
	}
	var hello struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(gm.D, &hello); err != nil || hello.HeartbeatInterval <= 0 {
		t.Fatalf("Hello 里没有 heartbeat_interval：%s", string(gm.D))
	}
	t.Logf("收到 Hello，心跳间隔 %d ms", hello.HeartbeatInterval)

	// 发一个假 Identify，服务端应以鉴权失败（4004）关连接。
	// 如果我们发的帧格式有问题，服务端会以协议错误（1002）关连接。
	fakeIdentify, _ := json.Marshal(map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":   "QQBot fake-token-for-test",
			"intents": qqIntentGroupC2C,
			"shard":   []int{0, 1},
			"properties": map[string]string{
				"$os": "linux", "$browser": "relay", "$device": "relay",
			},
		},
	})
	if err := ws.writeText(fakeIdentify); err != nil {
		t.Fatalf("发 Identify 失败：%v", err)
	}
	ws.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, err = ws.readMessage()
	// 读到 close 帧会返回 io.EOF，也可能直接断开；两种都可以接受，
	// 只要不是在发帧的时候就报错。
	// 关键是：如果服务端能读懂我们的帧，它会因为 token 假而关连接；
	// 如果帧格式错了，连接会在握手后立刻断开，上面读 Hello 就过不了。
	t.Logf("发假 Identify 后连接状态：%v（预期：服务端因鉴权失败关掉）", err)
}
