---
title: 接口响应约定
description: 业务接口统一响应结构与前端处理约定
---

# 接口响应约定

后端业务接口统一返回 JSON：

```json
{
  "code": 0,
  "data": {},
  "msg": "ok"
}
```

- `code`: 业务状态码，`0` 表示成功，非 `0` 表示失败。
- `data`: 业务数据。失败时通常为 `null`。
- `msg`: 响应消息。成功默认为 `ok`，失败时放错误原因。

前端请求逻辑以 `code` 判断业务是否成功。当前后端业务失败也会返回 HTTP 200，前端不要只依赖 HTTP 状态码判断结果。

接口连接失败、服务不可达、返回体不是约定 JSON 时，前端按网络或接口异常处理。

## AutoDL 渠道

- `POST /api/ai/autodl/workflows` 使用上述业务响应结构。请求为 `{ baseUrl, workflowId? }`，不传上游 API Key；不带工作流 ID 返回列表，带 ID 返回详情。元数据包含 `uuid`、`name`、`kind` 和详情的 `input_rules`，不返回上游内部节点映射。
- AutoDL 视频继续使用现有 `/api/v1/videos` 创建、查询及任务响应结构。
- `/api/v1/audio/speech` 是 AI 协议代理入口。AutoDL 成功时返回 `{ provider: "autodl", audio_url, mime_type }`；MiniMax 和 TokenDance MiniMax 成功时解码上游 hex，并返回所选格式的音频二进制；失败仍使用现有 `Fail` 业务结构。其他语音协议的响应保持原样。
- 现有 `/api/v1/canvas/audio-tasks` 消费 AutoDL 的上游音频 URL，返回既有任务结构，不新增结果下载或转存。

## 渠道自定传参转译

- 沿用 `/api/v1/images/generations`、`/api/v1/images/edits`、`/api/v1/audio/speech`、`/api/v1/videos` 及现有画布任务入口。命中自定义的账号请求携带 `{ model, n, _parameterTranslation: { kind, variables } }`；视频另携带面板 `seconds`，沿用现有按秒费用计算。`kind` 为 `image`、`video`、`audio`。
- `_parameterTranslation` 是系统内部负载。后端按账号或公共渠道读取保存的原文，转译后只向供应商发送配置里的方法、地址、请求头、查询参数和请求体。
- 图片 JSON 结果归一为 `{ data: [{ url }] }`；音频 URL 归一为 `{ provider: "parameter-translation", audio_url }`。配置直接返回文件时沿用二进制响应及 MIME。失败仍用现有 `Fail` 业务结构。
- 视频沿用现有任务响应，增加 `parameter_translation: true` 标记；私有查询快照和公共渠道脚本不随响应返回。创建、查询、完成和失败均进入现有任务管理，失败不重发默认请求。

## 用户 API Key

登录后从账户菜单进入“API Key”。每位用户最多保留 100 个未删除 Key，已过期 Key 也计入；撤销释放名额。密钥使用安全随机数生成，以 `ic_` 开头。数据库保留用于鉴权的 SHA-256 哈希，并以 AES-256-GCM 加密保存可取回副本；普通列表只返回前缀，登录用户可按需复制自己的完整 Key。

调用支持的业务接口时使用请求头，不能放进 URL：

```http
Authorization: Bearer ic_<your-api-key>
```

### 权限边界

API Key 使用所属用户的当前身份，同时受 Key 自己的权限范围限制；权限只收窄，不能提升账号权限。默认权限为 `read` 和 `generate`，各权限相互独立。

| 权限 | 允许的操作 |
| --- | --- |
| `read` | 查询画布、素材和生成记录、读取任务状态、公开素材/提示词的登录视图，以及明确列入白名单的状态批量查询、存储测量和 RunningHub 元数据检查 |
| `generate` | 图片生成/编辑、文本对话、Responses、语音、视频、图片/音频任务和工作流执行、工作流草稿生成；读取结果另需 `read` |
| `write` | 上传/登记文件、停止任务和上报调用日志；保存画布、工作流、技能、用户数据和生成记录会返回服务端数据，因此同时需要 `read` + `write` |
| `delete` | 删除本人资源记录，以及明确的批量删除接口；`write` 不包含 `delete`。写入本身仍允许修改或覆盖已授权数据内容 |

以下控制面只允许登录会话，Key 即使属于管理员也不能访问：

- `/api/admin/*` 管理后台。
- `/api/v1/api-keys` 的创建、列表、复制、撤销及调用记录查询；Key 不能派生新 Key，也不能取回任何完整 Key。
- `/api/v1/user-config` 及模型、存储配置读写，避免暴露或改写外部凭证。
- `/api/v1/comfy-bridges` 下的注册、查询、检查和删除。
- `POST /api/v1/canvas/projects/sync` 全量同步，其响应可能返回全部已有画布；脚本可使用单项目读写接口并配置所需权限。

Key 可调用的受保护接口由“HTTP 方法 + 路由模板”显式列出，新增接口默认不对 Key 开放。公开接口继续按原有规则访问；匿名存储 Cookie 与专用 Bridge Token 不由用户 Key 替代。个人 Bridge 认证还检查所属用户状态，用户禁用或删除后其 Bridge Token 不能继续认证；系统 Bridge 保持独立身份。

### 限流、额度与撤销

- `rateLimit`：每个 Key 每分钟请求上限，默认 60，范围 1～6000。窗口和计数持久化，多个服务进程共用，超出时返回 HTTP `429` 和 `Retry-After`。
- `requestLimit`：可选累计请求次数上限，`null` 表示不限；耗尽时返回 HTTP `403`。这不是金额额度，模型调用仍按现有规则消耗所属用户的算力点。
- 已通过凭证认证的请求尝试计入次数，包括权限拒绝和后续业务失败；限流、额度或凭证校验拒绝不增加累计次数。
- 用户禁用、删除和 Key 到期后，新的鉴权失败。撤销阻止后续准入，不取消已经准入的请求或已经开始的生成任务。
- 未配置权限的旧开发 Key 按无权限处理，需要用登录会话撤销重建，不会自动扩权。

### 管理接口

以下接口只接受登录会话，只操作当前用户的数据，响应使用 `{ code, data, msg }`，并禁止缓存：

- `GET /api/v1/api-keys`：`data` 为 `{ items, total }`，最多 100 条。
- `POST /api/v1/api-keys`：请求示例见下文。`expiresAt` 为 `null` 或省略时永久有效，指定时必须为未来 RFC3339 时间。省略 `scopes` 使用默认权限，显式空数组或未知权限会被拒绝。`data` 为 `{ apiKey, key }`，其中 `key` 是新创建的完整密钥。
- `POST /api/v1/api-keys/:id/reveal`：按需取回当前登录用户自己的完整密钥，`data` 为 `{ key }`。必须使用登录会话，API Key 凭证不能调用；已撤销、不存在或他人的 Key 返回 `404`，旧版仅存哈希的 Key 返回 `409` 并提示重建。
- `DELETE /api/v1/api-keys/:id`：撤销自己的 Key，成功时 `data` 为 `true`。
- `GET /api/v1/api-keys/:id/logs`：查询自己的 Key 最近 100 条调用记录，`data` 为 `{ items, total }`，`total` 是本次返回条目数。

```json
{
  "name": "自动化脚本",
  "expiresAt": null,
  "scopes": ["read", "generate"],
  "rateLimit": 60,
  "requestLimit": 10000
}
```

Key 元数据包含 `id`、`name`、`prefix`、`scopes`、`rateLimit`、`requestLimit`、`requestCount`、`createdAt`、`expiresAt`、`lastUsedAt` 和 `canCopy`；时间使用 RFC3339，`expiresAt`、`lastUsedAt` 未设置时为 `null`。列表接口不返回哈希、密文或完整密钥；`canCopy` 表示是否存在可取回的加密副本。

调用记录只保存 Key ID、用户 ID、方法、路由模板、HTTP 状态、耗时和时间，不保存请求头、请求体、查询参数或完整密钥。每用户最多保留最近 10000 条记录，自动淘汰旧记录；每个 Key 的查询界面最多展示其中最近 100 条。HTTP `200` 只代表该 HTTP 状态，仍需检查业务 `code`；任务查询还需检查任务状态。撤销 Key 保留审计记录，删除用户时清理其记录。

### 完整 Key 的加密与复制

服务端须配置独立的 `API_KEY_ENCRYPTION_KEY`：32 字节随机密钥，编码为 64 位十六进制，不与 `JWT_SECRET` 共用。可使用 `openssl rand -hex 32` 生成，在服务器私有环境文件中保存并备份。缺失或格式无效时，新 Key 创建和完整 Key 取回返回 `503`，不会降级保存明文；已有 Key 的哈希鉴权保持原规则。

密文以 `v1:` 标识版本，使用标准库 AES-256-GCM 的随机 nonce，附加认证数据绑定所有者与 Key ID。复制前验证密文完整性、原始 Key 格式和哈希，再写入不含秘密的审计记录；审计保存失败时返回 `503`，不披露明文。复制请求不占用 Key 的调用额度。

刷新或重新登录后可以再次复制新 Key。浏览器只在当前交互中处理明文，不写入持久化 store 或本地业务缓存。自动复制失败时提供临时手动复制入口，关闭后清理。创建和取回完整 Key 的响应使用 `Cache-Control: no-store` 与 `Pragma: no-cache`。

旧版只保存哈希，无法逆向恢复，列表显示不能复制并提示重新创建；升级不会自动替换、撤销或改变旧 Key。已经到期的加密 Key 仍允许所有者取回，但不会因此恢复调用权限。

加密主密钥必须跨重启、升级和回滚保留同值；恢复数据库时也需恢复对应主密钥。直接更换或丢失主密钥会导致既有密文无法取回，本版本不提供自动轮换或恢复机制，哈希鉴权本身不依赖该主密钥。

### 错误处理

- 凭证缺失、伪造、过期、撤销或用户失效：HTTP `401`、`code: 1`。
- Key 权限不足或访问会话专用接口：HTTP `403`、`code: 1`。
- Key 鉴权或限额数据库故障：HTTP `503`，不伪装成凭证错误，不进入业务处理。
- Key 限流：HTTP `429`，调用方按 `Retry-After` 等待；累计额度耗尽不可通过等待自动恢复。
- 现有普通业务接口仍可能返回 HTTP `200`、`code: 1`，外部调用方必须检查业务状态，不能只看 HTTP 状态。
- 通用 AI 转发在响应开始前遇到连接失败返回 `502`、网络超时返回 `504`；上游认证失败不会伪装成用户 Key 失效。上游限流保留 `429` 信号。
- 通用响应流的上游读取失败及显式 SSE 错误会记录为失败，按已有回调尝试退款；已经开始的响应不会再混入普通 JSON。客户端写入失败只记录交付失败，不据此自动退款，因为上游可能已经完成并产生费用，避免通过故意断开连接绕过计费。客户端断开后无法保证客户端收到错误提示。退款失败仍需通过业务日志核查，不应自动重发可能已经执行的付费生成请求。

示例：

```sh
curl https://your-canvas.example/api/auth/me \
  -H "Authorization: Bearer $CANVAS_API_KEY"

curl https://your-canvas.example/api/v1/canvas/projects \
  -H "Authorization: Bearer $CANVAS_API_KEY"
```

用户访问 Key 与模型渠道的上游 API Key 是两种凭证。此功能提供已有后端业务接口的认证与授权，浏览器本地直连仍按现有方式调用上游。
