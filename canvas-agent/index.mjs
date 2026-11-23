#!/usr/bin/env node
import { projectCredentials, startApiMcp } from './api.mjs'

// The web app starts its own MCP instance for tools bound to its open tab.
const externalMcp = process.argv[2] === 'mcp' && process.env.CANVAS_AGENT_SOURCE !== 'codex'
const credentials = externalMcp ? projectCredentials() : null
if (credentials) await startApiMcp(credentials)
else await import('./browser.mjs')
