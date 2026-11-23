/** Backend canvas operations. No browser, MCP transport or local Agent is used. */
import { createHash, randomUUID } from 'node:crypto'

export const revisionOf = (project) => createHash('sha256').update(JSON.stringify(project)).digest('hex')
const copy = (value) => structuredClone(value)
const id = () => randomUUID()
const fail = (message) => { throw new Error(message) }
const text = (value, name) => typeof value === 'string' && value.trim() ? value.trim() : fail(`${name} 不能为空`)
const nodeOf = (p, nodeId) => p.nodes.find((n) => n.id === nodeId) ?? fail(`节点不存在：${nodeId}`)
const selected = (p, ids) => {
  if (!Array.isArray(ids) || ids.some((v) => typeof v !== 'string')) fail('nodeIds 必须是节点 ID 数组')
  return [...new Set(ids)].map((v) => nodeOf(p, v))
}
const summary = (p) => ({ id: p.id, title: p.title, updatedAt: p.updatedAt, nodeCount: p.nodes.length, connectionCount: p.connections.length, revision: revisionOf(p) })
const READS = new Set(['summary', 'query', 'get', 'upstream', 'downstream', 'connected'])

function createNode(p, type, args) {
  const sizes = { text: [340, 240], image: [420, 236], video: [420, 236], audio: [340, 160] }
  const [width, height] = args.primary ? [550, 600] : sizes[type]
  const right = Math.max(0, ...p.nodes.filter((n) => !n.metadata?.groupId).map((n) => n.position.x + n.width))
  const node = { id: `${type}-${id()}`, type, title: text(args.title ?? args.prompt ?? type, 'title'), position: { x: Number.isFinite(args.x) ? args.x : right + 72, y: Number.isFinite(args.y) ? args.y : 0 }, width, height, metadata: { status: type === 'text' ? 'success' : 'idle' } }
  if (type === 'text') {
    if (typeof args.content !== 'string') fail('content 必须是字符串')
    node.metadata.content = args.content
    node.metadata.prompt = args.content
  } else node.metadata.prompt = args.prompt ?? ''
  const sources = selected(p, args.sourceNodeIds ?? [])
  const connections = sources.map((source) => ({ id: id(), fromNodeId: source.id, toNodeId: node.id }))
  p.nodes.push(node)
  p.connections.push(...connections)
  return { nodeId: node.id, connectionIds: connections.map((c) => c.id) }
}

function deleteNode(p, nodeId) {
  nodeOf(p, nodeId)
  const removed = new Set([nodeId])
  // Batch roots own their children; removing a group only ungroups its members.
  for (const n of p.nodes) if (removed.has(n.id)) for (const child of n.metadata?.batchChildIds ?? []) removed.add(child)
  for (const n of p.nodes) if (n.metadata?.isBatchRoot && n.metadata.batchChildIds?.length && n.metadata.batchChildIds.every((v) => removed.has(v))) removed.add(n.id)
  p.nodes = p.nodes.filter((n) => !removed.has(n.id))
  p.connections = p.connections.filter((c) => !removed.has(c.fromNodeId) && !removed.has(c.toNodeId))
  for (const n of p.nodes) {
    const m = n.metadata
    if (!m) continue
    if (removed.has(m.groupId)) delete m.groupId
    if (Array.isArray(m.batchChildIds)) {
      m.batchChildIds = m.batchChildIds.filter((v) => !removed.has(v))
      if (removed.has(m.primaryImageId)) {
        m.primaryImageId = m.batchChildIds[0]
        const primary = p.nodes.find((v) => v.id === m.primaryImageId)
        for (const key of ['content', 'storageKey', 'bytes', 'mimeType', 'naturalWidth', 'naturalHeight', 'status', 'imageTaskId', 'imageTaskResultId']) {
          if (primary?.metadata?.[key] === undefined) delete m[key]
          else m[key] = primary.metadata[key]
        }
      }
    }
  }
  for (const session of p.chatSessions ?? []) for (const message of session.messages ?? []) {
    for (const reference of message.references ?? []) if (removed.has(reference.id)) {
      for (const key of ['dataUrl', 'url', 'storageKey']) delete reference[key]
    }
  }
  return { deletedNodeIds: [...removed], retainedMediaFiles: true, retainedTasks: true }
}

export function editCanvas(p, args) {
  const a = args.action
  if (a === 'create_text' || a === 'create_primary_script') {
    const result = createNode(p, 'text', { ...args, primary: a === 'create_primary_script' })
    if (a === 'create_primary_script' && p.autoTitlePending) { p.title = text(args.projectTitle, 'projectTitle'); p.autoTitlePending = false }
    return result
  }
  if (a === 'create_media') {
    if (!['image', 'video', 'audio'].includes(args.kind)) fail('kind 必须是 image、video 或 audio')
    return createNode(p, args.kind, args)
  }
  if (a === 'update_text' || a === 'rename' || a === 'move') {
    const n = nodeOf(p, args.nodeId)
    if (a === 'update_text' && n.type !== 'text') fail('只有文本节点支持 update_text')
    if (args.title !== undefined) n.title = text(args.title, 'title')
    if (a === 'update_text' && args.content !== undefined) {
      if (typeof args.content !== 'string') fail('content 必须是字符串')
      n.metadata = { ...n.metadata, content: args.content, prompt: args.content, status: 'success' }
    }
    if (a === 'move') {
      if (![args.x, args.y].every(Number.isFinite)) fail('move 需要有限数值 x 和 y')
      if (n.metadata?.batchRootId || n.metadata?.isBatchRoot) fail('暂不支持单独移动批次节点，请使用网页的批次布局功能')
      const dx = args.x - n.position.x, dy = args.y - n.position.y
      if (n.type === 'group') for (const child of p.nodes.filter((v) => v.metadata?.groupId === n.id)) { child.position.x += dx; child.position.y += dy }
      n.position = { x: args.x, y: args.y }
    }
    return { nodeId: n.id }
  }
  if (a === 'delete') return deleteNode(p, args.nodeId)
  if (a === 'connect') {
    const from = nodeOf(p, args.fromNodeId), to = nodeOf(p, args.toNodeId)
    if (from.id === to.id || from.type === 'config' && to.type === 'config') fail('不允许自身连线或配置节点之间连线')
    const existing = p.connections.find((c) => c.fromNodeId === from.id && c.toNodeId === to.id)
    if (existing) return { connectionId: existing.id, alreadyExists: true }
    const c = { id: id(), fromNodeId: from.id, toNodeId: to.id }; p.connections.push(c)
    return { connectionId: c.id }
  }
  if (a === 'disconnect') {
    if (!p.connections.some((c) => c.id === args.connectionId)) fail('连线不存在')
    p.connections = p.connections.filter((c) => c.id !== args.connectionId)
    return { deletedConnectionId: args.connectionId }
  }
  if (a === 'group') {
    const members = selected(p, args.nodeIds)
    if (members.length < 2 || members.some((n) => n.type === 'group' || n.metadata?.groupId || n.metadata?.batchRootId || n.metadata?.isBatchRoot)) fail('分组需要至少两个未分组的普通节点，不支持嵌套组')
    const left = Math.min(...members.map((n) => n.position.x)), top = Math.min(...members.map((n) => n.position.y))
    const right = Math.max(...members.map((n) => n.position.x + n.width)), bottom = Math.max(...members.map((n) => n.position.y + n.height))
    const group = { id: `group-${id()}`, type: 'group', title: args.title || '组', position: { x: left - 24, y: top - 24 }, width: right - left + 48, height: bottom - top + 48, metadata: {} }
    for (const n of members) n.metadata = { ...n.metadata, groupId: group.id }
    p.nodes.push(group)
    return { groupId: group.id, nodeIds: members.map((n) => n.id) }
  }
  if (a === 'arrange') {
    const nodes = args.nodeIds?.length ? selected(p, args.nodeIds) : p.nodes.filter((n) => !n.metadata?.groupId && !n.metadata?.batchRootId)
    if (nodes.some((n) => n.metadata?.groupId || n.metadata?.batchRootId || n.metadata?.isBatchRoot)) fail('请整理顶层节点或整个组，避免拆散分组或批次')
    const startX = nodes.length ? Math.min(...nodes.map((n) => n.position.x)) : 0, startY = nodes.length ? Math.min(...nodes.map((n) => n.position.y)) : 0
    let x = startX, y = startY, rowHeight = 0
    nodes.forEach((n, index) => {
      if (index && index % 4 === 0) { x = startX; y += rowHeight + 72; rowHeight = 0 }
      const dx = x - n.position.x, dy = y - n.position.y
      if (n.type === 'group') for (const child of p.nodes.filter((v) => v.metadata?.groupId === n.id)) { child.position.x += dx; child.position.y += dy }
      n.position = { x, y }; x += n.width + 72; rowHeight = Math.max(rowHeight, n.height)
    })
    return { arrangedNodeIds: nodes.map((n) => n.id) }
  }
  fail('不支持的节点操作')
}

/** Each write checks a caller revision and reads back the saved project. Backend has no atomic CAS. */
export function createCanvasOperations(api) {
  const locks = new Map()
  const read = async (canvasId) => {
    const list = await api({ method: 'GET', path: '/canvas/projects' })
    const p = Array.isArray(list) && list.find((v) => v.id === canvasId)
    if (!p || !Array.isArray(p.nodes) || !Array.isArray(p.connections)) fail('画布不存在或数据结构无效，请先列出画布')
    return p
  }
  const exclusive = async (canvasId, run) => {
    const previous = locks.get(canvasId) ?? Promise.resolve()
    const next = previous.catch(() => {}).then(run)
    locks.set(canvasId, next)
    try { return await next } finally { if (locks.get(canvasId) === next) locks.delete(canvasId) }
  }
  const save = async (p, expected) => {
    if (revisionOf(await read(p.id)) !== expected) fail('画布版本冲突，请重新读取后再操作；未覆盖远端修改')
    p.updatedAt = new Date(Math.max(Date.now(), Date.parse(p.updatedAt) + 1 || 0)).toISOString()
    const stored = await api({ method: 'POST', path: '/canvas/projects', body: { data: p } })
    const verified = await read(p.id)
    if (!stored || revisionOf(stored) !== revisionOf(p) || revisionOf(verified) !== revisionOf(p)) fail('保存回读不一致，可能发生并发修改；请读取核对，不要盲目重试')
    return summary(verified)
  }
  const requireRevision = (p, expected) => {
    if (!expected || expected !== revisionOf(p)) fail('缺少或过期的 expectedRevision，请先读取当前画布')
  }
  const nodes = async (args) => {
    const run = async () => {
      const p = await read(text(args.canvasId, 'canvasId'))
      if (READS.has(args.action)) {
        let values = p.nodes
        if (args.action === 'get') values = [nodeOf(p, args.nodeId)]
        if (['upstream', 'downstream', 'connected'].includes(args.action)) {
          nodeOf(p, args.nodeId)
          const ids = new Set()
          for (const c of p.connections) {
            if (args.action !== 'downstream' && c.toNodeId === args.nodeId) ids.add(c.fromNodeId)
            if (args.action !== 'upstream' && c.fromNodeId === args.nodeId) ids.add(c.toNodeId)
          }
          values = values.filter((n) => ids.has(n.id))
        }
        if (args.action === 'query') values = values.filter((n) => (!args.nodeId || n.id === args.nodeId) && (!args.kind || n.type === args.kind) && (!args.keyword || `${n.title} ${n.type === 'text' ? n.metadata?.content ?? '' : ''}`.toLowerCase().includes(args.keyword.toLowerCase())))
        const offset = Math.max(0, args.offset ?? 0), limit = Math.min(100, Math.max(1, args.limit ?? 30))
        return { ok: true, project: summary(p), nodes: copy(values.slice(offset, offset + limit)), total: values.length, ...(args.action === 'summary' ? { connections: p.connections } : {}) }
      }
      requireRevision(p, args.expectedRevision)
      const changed = copy(p), result = editCanvas(changed, args)
      return { ok: true, ...result, project: await save(changed, args.expectedRevision), consistency: 'read-check-write-read; concurrent browser edits still require coordination' }
    }
    return READS.has(args.action) ? run() : exclusive(args.canvasId, run)
  }
  const rename = (args) => exclusive(args.canvasId, async () => {
    const p = await read(args.canvasId); requireRevision(p, args.expectedRevision)
    p.title = text(args.title, 'title')
    return { ok: true, project: await save(p, args.expectedRevision) }
  })
  const remove = (args) => exclusive(args.canvasId, async () => {
    const p = await read(args.canvasId); requireRevision(p, args.expectedRevision)
    await api({ method: 'POST', path: '/canvas/projects/delete', body: { ids: [p.id] } })
    const list = await api({ method: 'GET', path: '/canvas/projects' })
    if (list.some((item) => item.id === p.id)) fail('删除未通过回读确认')
    return { ok: true, deletedCanvasId: p.id }
  })
  const catalog = async ({ resource, skillId, path }) => {
    const routes = { models: { path: '/settings', public: true }, workflows: { path: '/workflows' }, skills: { path: '/agent-skills' }, system_skills: { path: '/agent-skills', public: true } }
    if (resource === 'skill_file') {
      if (!path || path.split('/').includes('..') || path.startsWith('/')) fail('skill 文件需要安全的相对路径')
      return { ok: true, data: await api({ method: 'GET', public: true, path: `/agent-skills/${encodeURIComponent(text(skillId, 'skillId'))}/file?path=${encodeURIComponent(path)}` }) }
    }
    if (!routes[resource]) fail('不支持的目录类型')
    const data = await api({ method: 'GET', ...routes[resource] })
    return { ok: true, data: resource === 'models' ? data?.modelChannel ?? data?.model_channel ?? {} : data }
  }
  const media = (args) => exclusive(args.canvasId, async () => {
    const p = await read(text(args.canvasId, 'canvasId')), n = nodeOf(p, args.nodeId)
    const kind = n.type
    if (!['image', 'audio', 'video'].includes(kind)) fail('媒体任务只支持 image、audio、video 节点')
    const m = n.metadata ??= {}
    const taskField = `${kind}TaskId`
    const taskPath = (taskId) => kind === 'video' ? `/videos/${encodeURIComponent(taskId)}` : `/canvas/${kind}-tasks/${encodeURIComponent(taskId)}`
    if (args.action === 'submit') {
      requireRevision(p, args.expectedRevision)
      if (m[taskField] || m.apiSubmission) fail('该节点已有提交记录，请查询任务；重新生成请创建新媒体节点，避免重复扣费')
      if (!args.request || typeof args.request !== 'object' || Array.isArray(args.request)) fail('request 需要符合后端接口格式的 JSON 对象')
      text(args.request.model, 'request.model'); text(args.channelId, 'channelId')
      if (Object.keys(args.request).some((k) => /^(api_?key|authorization|base_?url|access_?token|token)$/i.test(k))) fail('request 中不能包含凭证或渠道地址，请使用后端渠道 ID')
      const clientTaskId = `dsh-${id()}`
      const prompt = args.request.prompt ?? args.request.input
      if (typeof prompt === 'string') m.prompt = prompt
      m.generationMode = kind
      if (kind === 'image') m.generationType = args.edit ? 'edit' : 'generation'
      for (const field of ['size', 'quality', 'seconds']) if (args.request[field] !== undefined) m[field] = field === 'seconds' ? String(args.request[field]) : args.request[field]
      m.apiSubmission = 'submitting'; m.clientTaskId = clientTaskId; m[taskField] = clientTaskId; m.status = 'loading'; m.taskBackend = 'account'; m.model = args.request.model; m.channelId = args.channelId
      const marker = await save(p, args.expectedRevision)
      let task
      try {
        task = await api({ method: 'POST', path: kind === 'video' ? '/videos' : `/canvas/${kind}-tasks`, raw: kind === 'video', channelId: args.channelId, clientTaskId, sourceId: p.id,
          body: kind === 'video' ? args.request : { source: 'canvas', sourceId: p.id, nodeId: n.id, clientTaskId, channelId: args.channelId, prompt: args.request.prompt ?? args.request.input ?? m.prompt ?? '', endpoint: kind === 'audio' ? '/audio/speech' : args.edit ? '/images/edits' : '/images/generations', requestBody: args.request } })
      } catch (error) {
        return { ok: false, error: error.message, uncertainSubmission: true, canvasId: p.id, nodeId: n.id, taskId: clientTaskId, revision: marker.revision, hint: '提交结果不确定，不自动重发。先查询这个 taskId；确认没有执行后再决定是否创建新节点。' }
      }
      const next = await read(p.id)
      if (revisionOf(next) !== marker.revision) return { ok: false, error: '任务已提交，但画布发生并发修改，请按 taskId 查询恢复，禁止重发', taskId: task.id ?? clientTaskId }
      const metadata = nodeOf(next, n.id).metadata
      metadata[taskField] = task.id ?? clientTaskId; metadata.apiSubmission = 'submitted'
      return { ok: true, task, nodeId: n.id, project: await save(next, marker.revision) }
    }
    if (!m[taskField]) fail('节点没有后端任务 ID')
    if (args.action === 'stop') return { ok: true, taskId: m[taskField], data: await api({ method: 'POST', path: '/canvas/tasks/stop', body: { kind, id: m[taskField], workflow: false, source: 'canvas' } }) }
    if (!['status', 'sync'].includes(args.action)) fail('不支持的媒体任务操作')
    const task = await api({ method: 'GET', path: taskPath(m[taskField]), raw: kind === 'video' })
    if (args.action === 'status') return { ok: true, task, nodeId: n.id, project: summary(p) }
    requireRevision(p, args.expectedRevision)
    const url = task.url ?? task.image_url ?? task.audio_url ?? task.video_url ?? task.data?.[0]?.url
    const status = String(task.status).toLowerCase()
    m.status = ['completed', 'succeeded', 'success'].includes(status) ? url ? 'success' : 'error' : ['failed', 'error'].includes(status) ? 'error' : ['cancelled', 'canceled', 'stopped'].includes(status) ? 'stopped' : 'loading'
    m.progress = task.progress; m.apiSubmission = 'submitted'
    if (url) m.content = url
    if (m.status === 'error') m.errorDetails = task.error?.message ?? task.error_detail ?? '任务失败或未返回可用媒体地址'
    for (const key of ['storageKey', 'mimeType', 'bytes']) if (task[key] !== undefined) m[key] = task[key]
    if (task.width) m.naturalWidth = task.width
    if (task.height) m.naturalHeight = task.height
    return { ok: true, task, nodeId: n.id, project: await save(p, args.expectedRevision) }
  })
  return { read, nodes, rename, remove, catalog, media, summary }
}
