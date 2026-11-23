import { randomBytes } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { isAbsolute, resolve } from 'node:path'
import { Server } from '@modelcontextprotocol/sdk/server/index.js'
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js'
import { createCanvasOperations, revisionOf } from './canvas-operations.js'

export function projectCredentials(env = process.env) {
  const project = env.CANVAS_API_PROJECT
  let key = env.CANVAS_API_KEY?.trim()
  if (!key && !project) return null
  if (!env.CANVAS_API_URL) throw new Error('请设置与 Key 对应的 CANVAS_API_URL')
  const url = new URL(env.CANVAS_API_URL)
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
  if ((url.protocol !== 'https:' && !(loopback && url.protocol === 'http:')) ||
      url.pathname !== '/' || url.search || url.hash || url.username || url.password) {
    throw new Error('CANVAS_API_URL 应为 HTTPS 站点根地址；本机测试可使用 HTTP')
  }
  if (!key) {
    if (process.platform !== 'darwin' || !isAbsolute(project)) {
      throw new Error('CANVAS_API_PROJECT 需要 macOS 项目绝对路径；其他平台请使用 CANVAS_API_KEY')
    }
    const item = spawnSync('/usr/bin/security', ['find-generic-password',
      '-s', 'codex.canvas-agent.' + url.host, '-a', resolve(project), '-w'], { encoding: 'utf8' })
    if (item.status === 44) return null // errSecItemNotFound
    if (item.status !== 0) throw new Error('无法读取本项目的 macOS 钥匙串凭据')
    key = item.stdout.trim()
  }
  if (!/^ic_[A-Za-z0-9_-]+$/.test(key)) throw new Error('画布 API Key 格式无效')
  return { site: url.origin, key }
}

async function canvasApi(site, key, request) {
  const target = new URL(site + (request.public ? '/api' : '/api/v1') + request.path)
  const headers = request.public ? {} : { authorization: `Bearer ${key}` }
  if (request.channelId) headers['X-Model-Channel-ID'] = request.channelId
  if (request.clientTaskId) headers['X-Client-Video-Task-ID'] = request.clientTaskId
  if (request.sourceId) { headers['X-Video-Task-Source'] = 'canvas'; headers['X-Video-Task-Source-ID'] = request.sourceId }
  if (request.body !== undefined) headers['content-type'] = 'application/json'
  let response
  try {
    response = await fetch(target, {
      method: request.method,
      headers,
      redirect: 'error',
      body: request.body === undefined ? undefined : JSON.stringify(request.body),
      signal: AbortSignal.timeout(30_000),
    })
  } catch {
    throw new Error('画布站不可达，请检查站点地址与网络；认证请求不允许重定向')
  }
  if (response.status === 401) throw new Error('API Key 无效、已过期或已撤销，请更换 Key')
  if (response.status === 403) throw new Error('API Key 权限不足或请求额度耗尽；读取需 read，保存需 read + write，生成需 generate，资源删除需 delete')
  if (response.status === 429) throw new Error(`API Key 请求过于频繁，请在 ${response.headers.get('retry-after') ?? '60'} 秒后重试`)
  let envelope
  try {
    envelope = await response.json()
  } catch {
    throw new Error(`画布站返回异常（HTTP ${response.status}）`)
  }
  if (!response.ok || envelope?.code !== undefined && envelope.code !== 0 || !request.raw && envelope?.code !== 0 || request.raw && envelope?.error && typeof envelope.status !== 'string') {
    const detail = envelope?.msg ?? envelope?.error?.message
    const message = typeof detail === 'string' ? detail.replaceAll(key, '<key>') : ''
    throw new Error(message || `画布站请求失败（HTTP ${response.status}）`)
  }
  return envelope.code === 0 ? envelope.data : envelope
}

function newCanvasId() {
  return randomBytes(16).toString('base64url').replace(/[^A-Za-z0-9_-]/g, '').slice(0, 21)
}

/** A canvas project shaped like the ones the Web app saves. */
function newCanvasProject(title) {
  const now = new Date().toISOString()
  return {
    id: newCanvasId(),
    title,
    createdAt: now,
    updatedAt: now,
    nodes: [],
    connections: [],
    chatSessions: [],
    activeChatId: null,
    agentConfig: null,
    autoTitlePending: true,
    backgroundMode: 'lines',
    showImageInfo: false,
    viewport: { x: 0, y: 0, k: 1 },
    sidePanel: { open: true, width: 280 },
    agentPanel: { open: false, width: 464 },
  }
}

export async function startApiMcp({ site, key }) {
  const api = (request) => canvasApi(site, key, request)
  const backend = createCanvasOperations(api)
  const tools = new Map()
  const canvasUrl = (id) => site + '/canvas/' + encodeURIComponent(id)
  const register = (name, description, properties, required, execute) => {
    tools.set(name, { definition: { name, description, inputSchema: { type: 'object', properties, required, additionalProperties: false } }, execute })
  }
  register('infinite_canvas_status', 'Check authenticated access to the saved canvases of this deployment (read only).', {}, [], async () => {
    const projects = await api({ method: 'GET', path: '/canvas/projects' })
    if (!Array.isArray(projects)) throw new Error('画布列表响应格式异常')
    return { ok: true, mode: 'api-key', site, projectCount: projects.length }
  })
  const string = { type: 'string' }, strings = { type: 'array', items: string }
  register('infinite_canvas_projects', 'List, create, rename or delete saved backend canvases. No browser navigation. open only returns an optional viewing URL. Deleting a canvas needs delete scope and explicit user intent.', {
    action: { type: 'string', enum: ['list', 'create', 'rename', 'delete', 'open'] }, canvasId: string, title: string, expectedRevision: string,
  }, ['action'], async (args) => {
    if (args.action === 'rename') return backend.rename(args)
    if (args.action === 'list') {
      const projects = await api({ method: 'GET', path: '/canvas/projects' })
      return { ok: true, projects: projects.map((p) => ({ ...backend.summary(p), canvasUrl: canvasUrl(p.id) })) }
    }
    if (args.action === 'create') {
      const p = newCanvasProject(args.title?.trim().slice(0, 80) || '未命名画布')
      const saved = await api({ method: 'POST', path: '/canvas/projects', body: { data: p } })
      const verified = await backend.read(p.id)
      if (revisionOf(saved) !== revisionOf(verified)) throw new Error('新建后的回读不一致，请先读取核对，不要重复创建')
      return { ok: true, created: { ...backend.summary(verified), canvasUrl: canvasUrl(p.id) } }
    }
    const p = await backend.read(args.canvasId)
    if (args.action === 'open') return { ok: true, canvasUrl: canvasUrl(p.id), browserRequired: false }
    if (args.action === 'delete') return backend.remove(args)
    throw new Error('不支持的画布操作')
  })
  register('infinite_canvas_nodes', 'Read and edit saved canvas nodes, connections, groups and layout via backend API. All writes require expectedRevision from a fresh read. The server rejects stale project saves. There is no browser selection or implicit current canvas. Delete removes graph references but retains media files and backend tasks.', {
    action: { type: 'string', enum: ['summary', 'query', 'get', 'upstream', 'downstream', 'connected', 'create_text', 'create_primary_script', 'create_media', 'update_text', 'rename', 'move', 'delete', 'connect', 'disconnect', 'group', 'arrange'] },
    canvasId: string, expectedRevision: string, nodeId: string, nodeIds: strings, sourceNodeIds: strings, title: string, projectTitle: string, content: string, prompt: string, keyword: string,
    kind: { type: 'string', enum: ['text', 'image', 'video', 'audio', 'config', 'panorama', 'director', 'group'] },
    fromNodeId: string, toNodeId: string, connectionId: string, x: { type: 'number' }, y: { type: 'number' }, offset: { type: 'integer', minimum: 0 }, limit: { type: 'integer', minimum: 1, maximum: 100 },
  }, ['action', 'canvasId'], backend.nodes)
  register('infinite_canvas_catalog', 'Read backend public model/channel options, saved workflows, personal/system skills or a system skill file. User model/storage credentials and local browser configuration are deliberately unavailable.', {
    resource: { type: 'string', enum: ['models', 'workflows', 'skills', 'system_skills', 'skill_file'] }, skillId: string, path: string,
  }, ['resource'], backend.catalog)
  register('infinite_canvas_media', 'Explicit backend media task submission, read-only status, sync result into a prepared media node, or stop task. Submit only on user-authorized generation; request must use the backend provider JSON contract with explicit model and channelId, never provider credentials. Create a media node first. submit and sync need expectedRevision. Do not retry uncertain submissions or reuse a submitted node. Does not reproduce browser-local providers or workflows automatically.', {
    action: { type: 'string', enum: ['submit', 'status', 'sync', 'stop'] }, canvasId: string, nodeId: string, expectedRevision: string,
    channelId: string, edit: { type: 'boolean' }, request: { type: 'object', additionalProperties: true },
  }, ['action', 'canvasId', 'nodeId'], backend.media)

  const server = new Server({ name: 'infinite-canvas', version: '0.1.4' }, { capabilities: { tools: {} } })
  server.setRequestHandler(ListToolsRequestSchema, () => ({ tools: [...tools.values()].map(tool => tool.definition) }))
  server.setRequestHandler(CallToolRequestSchema, async ({ params }) => {
    try {
      const tool = tools.get(params.name)
      if (!tool) throw new Error('未知画布工具')
      const result = await tool.execute(params.arguments ?? {})
      return { isError: result.ok === false, content: [{ type: 'text', text: JSON.stringify(result).replaceAll(key, '[redacted]') }] }
    } catch (error) {
      return { isError: true, content: [{ type: 'text', text: error.message.replaceAll(key, '[redacted]') }] }
    }
  })
  await server.connect(new StdioServerTransport())
}
