# Gemini 网页版渠道

`gemini-web` 使用已登录的 `https://gemini.google.com` 会话，直接调用网页文本接口。它与 Gemini API Key、Gemini CLI 和 AI Studio 渠道独立。

## 导入账号

1. 在 Ego 浏览器中打开 Gemini，手动完成 Google 登录，将浏览器控制权交回。
2. 运行导出脚本，填写任务空间编号和账号文件绝对路径：

```bash
ego-browser nodejs <<'EOF'
globalThis.cpaGeminiExport = {
  spaceId: 18,
  authFile: '/absolute/path/to/auths/gemini-web-account.json',
  account: 'gemini-web-account',
  proxy: 'http://127.0.0.1:7990'
};
await import('/absolute/path/to/CLIProxyAPIPlus/scripts/export-gemini-web-auth.mjs');
EOF
```

任务空间编号以打开浏览器时输出的 `spaceId` 为准。代理可省略；填写时必须是运行 CPA 的机器能够访问的代理地址。

脚本只导出 `__Secure-1PSID` 和 `__Secure-1PSIDTS`，以 `0600` 权限创建文件，拒绝覆盖已有文件。重新登录后可导出到新文件，再替换对应账号。不要将账号文件提交到 Git 或粘贴到日志中。

账号文件格式如下，值均为占位符：

```json
{
  "type": "gemini-web",
  "email": "gemini-web-account",
  "secure_1psid": "GOOGLE_SESSION_COOKIE",
  "secure_1psidts": "GOOGLE_SESSION_TIMESTAMP_COOKIE",
  "proxy_url": "http://127.0.0.1:7990"
}
```

将文件放入配置的 `auth-dir`，或通过已有的管理端账号文件上传功能导入。服务器需要运行包含该渠道的新版二进制；文件监听器会自动注册账号及模型。登录过期时重新登录并导入，不需要提供 Google 密码给 CPA。

## 模型与调用

管理后台的账号详情 → 配置提供账号显示名称、JSON 文件名及两个 Session 值的编辑项。Session 输入框留空保留现值，保存后会清空，原始记录中的 Cookie 始终隐藏。文件名必须以 `.json` 结尾且不能包含路径，目标文件已存在或源文件被其他操作修改时会拒绝重命名；成功后关闭旧详情并刷新账号列表。修改 Session 会清除旧凭据导致的 401 状态。

文件重命名使用 `/v8/management/credentials/rename`，需要前后端都更新到支持此功能的版本。

根据 2026-10-14 登录后的网页模式选择器建立以下映射；实际权限取决于账号：

| API 模型 ID | 网页模式 |
| --- | --- |
| `gemini-web-3.5-flash-lite` | 3.5 Flash-Lite |
| `gemini-web-3.8-flash` | 3.8 Flash |
| `gemini-web-3.1-pro` | 3.1 Pro |
| `gemini-web-4-argon` | 4 Argon |

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-web-3.5-flash-lite","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

文本请求通过现有转换器兼容 Chat Completions、Responses、Claude Messages 和 Gemini GenerateContent；支持普通响应及流式文本增量。

## 行为与限制

- 每次 API 请求创建独立网页对话，并将传入文本历史按角色串联。不会复用用户提供的已有网页对话；系统指令通过文本表达，不具备原生 API 的独立系统消息通道。
- 图片、文件、音频、视频、原生工具调用和结构化输出不受支持；检测到这些输入时返回错误。
- 网页不提供可靠的 Token 用量和计数接口。渠道不伪造计费数据，Token 计数返回 `501`。
- 网页接口没有映射 `temperature`、`max_tokens` 等采样和长度参数，也未映射思考等级。客户端不能依靠这些参数约束生成。
- 登录态可能过期，网页协议和模式 ID 也可能变化。账号权限不足、限额或协议变化会导致请求失败。
- 生成后返回的 Google Cookie 和其他私有响应头不会透传给 API 客户端。账号刷新会重新读取网页初始化信息并保留服务器返回的 Cookie。
- 不记录带 Cookie、表单令牌的原始网页请求；已有请求统计仍记录成功、失败和模型。

## Payload 配置

规则匹配 `protocol: gemini-web`，作用于最终的网页 JSON 数组；例如 `0.0` 是提示文本。应用规则后只进行表单编码，不再修改业务字段。

```yaml
payload:
  override:
    - models:
        - name: gemini-web-*
          protocol: gemini-web
      params:
        '0.0': '仅用于诊断的固定提示'
```

这是网页内部协议的高级配置，数组位置可能随 Google 网页升级变化。
