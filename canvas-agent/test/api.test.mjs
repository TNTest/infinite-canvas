import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js'
import { projectCredentials } from '../api.mjs'

const key = 'ic_fixture'
const site = 'https://canvas.example.com'

test('API credentials belong to an explicit deployment', () => {
  assert.equal(projectCredentials({}), null)
  assert.deepEqual(projectCredentials({ CANVAS_API_URL: site, CANVAS_API_KEY: key }), { site, key })
  for (const url of [undefined, 'http://canvas.example.com', site + '/other', 'https://user@canvas.example.com']) {
    assert.throws(() => projectCredentials({ CANVAS_API_URL: url, CANVAS_API_KEY: key }))
  }
})

test('the external MCP uses the key without a browser and keeps API errors in API mode', async (t) => {
  const projects = new Map()
  let denied = false
  const requests = []
  const server = createServer(async (req, res) => {
    requests.push({ path: req.url, method: req.method, authorization: req.headers.authorization })
    res.setHeader('content-type', 'application/json')
    if (denied) {
      res.writeHead(403).end('{}')
    } else if (req.method === 'GET') {
      res.end(JSON.stringify({ code: 0, data: [...projects.values()] }))
    } else {
      const chunks = []
      for await (const chunk of req) chunks.push(chunk)
      const { data, expectedUpdatedAt } = JSON.parse(Buffer.concat(chunks))
      const current = projects.get(data.id)
      if (current && expectedUpdatedAt !== current.updatedAt) {
        res.writeHead(409).end(JSON.stringify({ code: 1, msg: '画布版本冲突' }))
        return
      }
      const saved = { ...data, updatedAt: new Date(Date.parse(data.updatedAt) + 1).toISOString() }
      projects.set(data.id, saved)
      res.end(JSON.stringify({ code: 0, data: saved }))
    }
  })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  t.after(() => new Promise(resolve => server.close(resolve)))

  const client = new Client({ name: 'canvas-agent-test', version: '1' })
  await client.connect(new StdioClientTransport({
    command: process.execPath,
    args: [fileURLToPath(new URL('../index.mjs', import.meta.url)), 'mcp'],
    env: { CANVAS_API_URL: 'http://127.0.0.1:' + server.address().port, CANVAS_API_KEY: key },
  }))
  t.after(() => client.close())
  const names = (await client.listTools()).tools.map(tool => tool.name)
  assert.ok(names.includes('infinite_canvas_projects') && names.includes('infinite_canvas_media'))
  const call = async (name, args) => {
    const result = await client.callTool({ name, arguments: args })
    assert.equal(result.isError, false, result.content[0].text)
    return JSON.parse(result.content[0].text)
  }
  const { created } = await call('infinite_canvas_projects', { action: 'create', title: 'Test' })
  const node = await call('infinite_canvas_nodes', {
    action: 'create_text', canvasId: created.id, expectedRevision: created.revision, content: 'Scene',
  })
  assert.equal(node.project.nodeCount, 1)
  denied = true
  const result = await client.callTool({ name: 'infinite_canvas_status', arguments: {} })
  assert.equal(result.isError, true)
  assert.match(result.content[0].text, /权限/)
  assert.ok(requests.every(req => req.path === '/api/v1/canvas/projects' && req.authorization === 'Bearer ' + key))
})
