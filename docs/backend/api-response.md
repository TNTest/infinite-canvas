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

登录后从账户菜单进入“API Key”，创建用于脚本或外部应用的访问密钥。每位用户最多保留 100 个 Key（已过期但未删除的 Key 也计入），删除后释放名额。Key 完整内容仅创建时返回一次，列表只返回名称和前缀。

调用现有接口时使用请求头：

```http
Authorization: Bearer ic_<your-api-key>
```

Key 使用所属用户的当前身份，可调用该用户有权访问的全部后端接口，包括管理自己的 Key。普通用户 Key 无法访问管理后台接口；管理员 Key 继承管理员权限。用户被禁用、删除或角色变化时，后续请求立即按当前状态判断。模型调用沿用用户原有的配置、算力点和资源归属规则。

### 管理接口

以下接口均要求登录凭证或有效 API Key，只操作当前用户的 Key，响应沿用 `{ code, data, msg }`：

- `GET /api/v1/api-keys`：`data` 为 `{ items, total }`，最多 100 条。
- `POST /api/v1/api-keys`：请求 `{ "name": "自动化脚本", "expiresAt": null }`。`expiresAt` 为 `null` 或省略时永久有效；指定时必须为未来的 RFC3339 时间，例如 `2099-01-01T00:00:00Z`。`data` 为 `{ apiKey, key }`，其中 `key` 是唯一一次返回的完整密钥。
- `DELETE /api/v1/api-keys/:id`：删除并立即撤销自己的 Key，成功时 `data` 为 `true`；可以删除当前请求使用的 Key，撤销从后续请求生效。

`apiKey` 和列表中的条目均包含 `id`、`name`、`prefix`、`createdAt`、`expiresAt` 和 `lastUsedAt`。后两项可为 `null`，时间采用 RFC3339。哈希值和所属用户 ID 不通过这些响应公开。

示例（将环境变量设置为创建时保存的 Key）：

```sh
curl https://your-canvas.example/api/auth/me \
  -H "Authorization: Bearer $CANVAS_API_KEY"

curl https://your-canvas.example/api/v1/canvas/projects \
  -H "Authorization: Bearer $CANVAS_API_KEY"
```

这里的用户访问 Key 与模型渠道的上游 API Key 是两种凭证。该功能提供现有后端接口的身份认证；浏览器本地直连的模型渠道仍按现有方式访问上游，不会因创建用户 Key 而新增代理接口。
