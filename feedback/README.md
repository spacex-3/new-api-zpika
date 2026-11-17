# 独立留言反馈服务

该目录是一个独立 Go 服务，不依赖 new-api 数据库。它提供：

- 公开留言页 `/` 和可嵌入页面 `/embed`
- 必填邮箱与反馈内容、提交频率限制和蜜罐字段
- Bark 即时通知，标准设备地址使用 UTF-8 JSON 推送
- 公开页可按邮箱查询历史留言并继续回复
- 管理后台 `/admin`，可查看反馈、回复客户并更新处理状态
- AES-256-GCM 加密持久化

## 初始化

复制 `compose.example.yml` 为实际 Compose 配置后，先填写其中的域名：

- `ALLOWED_HOST`：反馈站域名，不含协议
- `PUBLIC_ORIGIN`：反馈站完整 HTTPS Origin
- `EMBED_PARENT_ORIGINS`：允许嵌入该页面的 new-api Origin；多个地址用逗号分隔
- `TRUST_CLOUDFLARE`：仅当服务端口禁止公网直连、请求一定经过可信 Cloudflare/反向代理时设为 `true`

创建服务所需的秘密文件：

```bash
mkdir -p secrets
chmod 700 secrets
umask 077
openssl rand -base64 32 > secrets/encryption-key
openssl rand -base64 32 > secrets/session-key
admin_token="$(openssl rand -base64 32 | tr -d '\n')"
printf '%s' "$admin_token" | sha256sum | cut -d ' ' -f 1 > secrets/admin-token-hash
printf '%s' 'https://api.day.app/你的设备密钥' > secrets/bootstrap-bark-url
printf '管理员口令：%s\n' "$admin_token"
```

管理员口令只在初始化时显示，应保存到密码管理器。Bark 地址也可以先留空，之后在管理后台配置。

## 启动与检查

```bash
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8080/healthz
```

生产环境应由反向代理提供 HTTPS，并仅将服务端口暴露给本机或内部 Docker 网络。

## 数据保留

反馈和 Bark 配置保存在 `feedback-data` 命名卷中的加密状态文件。服务不会自动删除反馈；容器重建或升级也不会清空该卷。删除 Compose 项目时不要使用 `docker compose down -v`，除非已经备份并确认要永久删除全部反馈。

公开页按邮箱精确匹配历史留言，但不会验证邮箱所有权。该模式适合一般客服反馈，不应要求用户在留言中提交 API 密钥、密码等敏感信息；需要更高隐私等级时，应先接入邮件验证码或登录态再开放历史查询。

## 接入 new-api

在 new-api 系统设置中填写反馈页 URL，例如：

```text
https://feedback.example.com/embed
```

然后开启“向用户显示反馈页面”。管理员不受该开关限制，普通用户只有在开关开启后才会看到侧栏入口。
