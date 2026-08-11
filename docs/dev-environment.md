# UNICA 开发环境信息

## 微信公众号测试账号

| 项目 | 值 |
|------|---|
| AppID | `wx2b53de720673da7a` |
| AppSecret | `<REDACTED-见带外凭证库>` |
| Token | `<REDACTED-见带外凭证库>` |
| Webhook URL | `https://ailurus.top/webhook/wechat` |
| 加密模式 | 未启用（明文模式） |
| 验证状态 | 已通过 (2026-03-05) |
| 消息推送 | 已验证可收到消息 |

### 使用方式
1. 登录测试号管理页面：`https://mp.weixin.qq.com/debug/cgi-bin/sandbox`
2. 用微信扫码关注测试号二维码
3. 在微信中给测试号发消息即可触发 webhook

---

## 服务器信息（临时测试用）

| 项目 | 值 |
|------|---|
| 域名 | `ailurus.top` |
| IP | `31.97.147.202` |
| SSH 端口 | `12222` |
| 用途 | Nginx 反向代理，临时 webhook 验证 |
| 注意 | 非部署服务器，VPN 也在此服务器上，勿动 |

### Nginx 配置
已在 `/etc/nginx/sites-enabled/ailurus.top` 添加：
```nginx
location /webhook/wechat {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 10s;
}
```

SSL 证书：Let's Encrypt，自动续期。

---

## 本机开发环境

| 项目 | 值 |
|------|---|
| OS | Windows 11 + WSL |
| Go | 1.23+ |
| 项目路径 | `E:\kefu\unica\` |
| Gateway 端口 | 8080 (默认) |

### 环境变量（Gateway 启动需要）
```bash
export WECHAT_APP_ID=wx2b53de720673da7a
export WECHAT_APP_SECRET=<REDACTED-见带外凭证库>   # 真值见带外凭证库,勿写回本文档
export WECHAT_TOKEN=<REDACTED-见带外凭证库>        # 同上
export WECHAT_ENCRYPTED_MODE=false
export WECHAT_CHANNEL_ID=wechat-test
export REDIS_URL=redis://localhost:6379/0
export GATEWAY_PORT=8080
```

---

## 已完成的代码 (STORY-007 Phase 1)

### 新增文件
- `unica/gateway/internal/adapter/wechat/adapter.go` — ChannelAdapter 实现
- `unica/gateway/internal/adapter/wechat/crypto.go` — SHA1 签名验证 + AES 解密
- `unica/gateway/internal/adapter/wechat/xml.go` — XML 消息类型定义
- `unica/gateway/internal/adapter/wechat/handler.go` — Webhook HTTP 处理器
- `unica/gateway/internal/adapter/wechat/crypto_test.go` — 签名验证测试

### 修改文件
- `unica/gateway/internal/config/config.go` — 添加 WeChat 配置项
- `unica/gateway/cmd/gateway/main.go` — 注册 /webhook/wechat 路由

### 编译状态
- `go build ./...` 通过
- `go test ./internal/adapter/wechat/` 通过

---

## 待开发（Sprint 2 后续）

| 顺序 | Story | 说明 |
|------|-------|------|
| 1 | STORY-010 | Token Manager — WeChat adapter 发回复需要 access_token |
| 2 | STORY-009 | 消息去重 + 死信队列 |
| 3 | STORY-007 Phase 2 | 接入 Token Manager，完整收发消息测试 |
| 4 | STORY-015 | 会话状态机 + 数据库表 |
| 5 | STORY-016 | 智能路由 |
