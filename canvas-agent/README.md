# Canvas Agent

Codex 插件优先使用用户 API Key 操作服务端已保存的画布。没有 Key 时，可以通过本地 Agent 连接浏览器中的画布。网页内发起的 Codex 对话沿用当前标签页的工具。

## 安装 Codex 插件

在 Codex 所在电脑执行：

```sh
codex plugin marketplace add https://github.com/TNTest/infinite-canvas.git
codex plugin add canvas-agent@infinite-canvas
```

需要 Node.js 22+ 和 npm。GitHub 插件从本仓库取得源码，用 npx 安装本地包及依赖。npm 上已发布的 0.1.3 可单独用作网页桥接服务。安装后新建 Codex 对话，移除插件可运行 `codex plugin remove canvas-agent`。

## API Key 模式

在启动 Codex 的环境中提供 `CANVAS_API_URL`（部署站点的 HTTPS 根地址）和 `CANVAS_API_KEY`（站点签发的用户 Key）。macOS 也可以设置 `CANVAS_API_PROJECT` 为项目绝对路径，读取钥匙串中服务为 `codex.canvas-agent.<站点主机名及可选端口>`、账户为该路径的通用密码。环境变量中的 Key 优先。远程请求使用 HTTPS 和 Authorization 请求头；本机 HTTP 仅用于测试。认证失败时报告后端错误。

在新对话中说“列出我的无限画布”，或调用 `infinite_canvas_status` 验证连接。

| MCP 工具 | 用途 |
| --- | --- |
| `infinite_canvas_projects` | 列出、新建、重命名、打开或删除画布 |
| `infinite_canvas_nodes` | 读取及编辑节点、分组、连线和布局 |
| `infinite_canvas_catalog` | 查询模型渠道、已保存工作流与技能 |
| `infinite_canvas_media` | 提交、查询、同步或停止媒体任务 |

编辑时指定画布 ID 和最新读取的 `expectedRevision`。服务端会拒绝基于旧版本的保存。版本冲突时重新读取画布，核对修改是否仍适用。实际生成使用服务端渠道并可能消耗额度。提交结果不确定时先按任务 ID 查询。

### 仅在一个 Codex 项目中启用

可以把本仓库克隆到本机，在目标项目的 `.codex/config.toml` 中添加以下配置。将示例路径替换为本仓库检出目录；从该受信任项目新建对话即可加载插件。

```toml
[marketplaces.infinite-canvas]
source_type = "local"
source = "/absolute/path/to/infinite-canvas"

[plugins."canvas-agent@infinite-canvas"]
enabled = true
```

此配置控制插件的加载范围。服务端仍按所属用户和 Key 权限控制画布访问。如只需注册 MCP，并希望在项目配置中绑定特定站点和 macOS 钥匙串条目，可以单独使用以下配置：

```toml
[mcp_servers.infinite-canvas]
command = "npx"
args = ["--yes", "--ignore-scripts", "--install-links", "--package=.", "canvas-agent", "mcp"]
cwd = "/absolute/path/to/infinite-canvas/canvas-agent"
env_vars = ["CANVAS_API_KEY"]

[mcp_servers.infinite-canvas.env]
CANVAS_API_URL = "https://canvas.example.com"
CANVAS_API_PROJECT = "/absolute/path/to/your-project"
```

## 网页桥接

安装 Node.js 22+ 后，在终端执行，npx 会从 npm 获取服务及依赖：

```sh
npx -y @tigerowo/canvas-agent@0.1.3
```

首次启动自动生成 Token，保存在当前用户的 `~/.infinite-canvas/codex-agent.json`；下次启动复用。终端显示 `Local URL` 和 `Connect token`，默认地址为 `http://127.0.0.1:3210`。网页与服务在同一台电脑运行，不需要克隆仓库或手工创建 `.env`；正确 Token 连接后自动记录网站来源。

Codex 尚未登录时执行 `npx -y @openai/codex@0.159.2 login` 并完成登录，已有登录态会直接复用。

## 本机配置与范围

`npx -y @tigerowo/canvas-agent@0.1.3 config` 输出 `{url, token, origins}`，首次尚未启动时会提示先启动服务。HTTP `GET /config` 只返回 `{url, hasToken}`；Token 和包含它的连接链接不要分享。

`CANVAS_AGENT_TOKEN`、`CANVAS_AGENT_PORT`、`CANVAS_AGENT_ORIGINS` 可选环境变量覆盖 Token、端口和预置来源；更改后重启服务。通常无需设置。

网页桥接模式的外部 MCP 默认只操作唯一已连接的画布；多个画布连接时，通过 MCP 环境变量 `CANVAS_AGENT_CLIENT_ID` 明确目标。断开连接会结束对应 Codex 进程，已提交的生成任务仍由现有画布流程处理。

默认权限为 `workspace-write` 与 `on-request`，工作目录为实际 Agent 包目录。服务不写 Codex 全局配置文件；其他全局 MCP 是否合并到当前会话尚未联调确认。
