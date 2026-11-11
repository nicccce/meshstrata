// Run after integration with: node controller/web/src/features/activity/activity-model.test.mjs
// Kept separate from the shared checker, which this feature does not own.
import assert from 'node:assert/strict'
import { readFile, mkdtemp, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import ts from '../../../node_modules/typescript/lib/typescript.js'
const temp = await mkdtemp(join(tmpdir(), 'meshstrata-activity-'))
try {
  for (const [input, output] of [['./activity-model.ts', 'model'], ['../../demo/seeds.ts', 'seeds']]) {
    const source = await readFile(new URL(input, import.meta.url), 'utf8')
    await writeFile(join(temp, `${output}.mjs`), ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText)
  }
  const { simulateReleaseAttempt, dependencyPlan, tailText, canCancel, canRetry } = await import(pathToFileURL(join(temp, 'model.mjs')))
  const { createSeed } = await import(pathToFileURL(join(temp, 'seeds.mjs')))
  const now = '2026-11-10T01:00:00Z'
  const ws = createSeed()
  const original = structuredClone(ws)
  const first = simulateReleaseAttempt(ws.releases[0], ws, 'retry', now)
  assert.equal(first.state, 'rollback_pending')
  assert.equal(first.nodes.find(node => node.nodeId === 'offline').rollback, 'pending')
  assert.equal(first.nodes.find(node => node.nodeId === 'hk').rollback, 'confirmed')
  assert.deepEqual(ws, original, 'pure retry must preserve all source revisions, identities, refs and ports')
  ws.nodes.find(node => node.id === 'offline').control = 'online'
  const restored = simulateReleaseAttempt(first, ws, 'retry', now)
  assert.equal(restored.state, 'rolled_back')
  assert.equal(restored.cleanup, 'pending')
  const cleaned = simulateReleaseAttempt(restored, ws, 'cleanup', now)
  assert.equal(cleaned.cleanup, 'pending', 'referenced resource ports must remain allocated')
  assert.equal(cleaned.state, 'rolled_back', 'cleanup must preserve the application outcome')
  assert.deepEqual(cleaned.nodes, restored.nodes)
  const reverse = { ...ws.releases[0], id: 'reverse-test', routeId: 'reverse-route', state: 'failed', cleanup: 'none', nodes: [], stages: undefined }
  assert.deepEqual(dependencyPlan(reverse, ws).map(step => step.id), ['save', 'validate', 'remote-listener', 'relay-authorization', 'tunnel-mapping', 'mapping-auth', 'entry', 'listeners-auth', 'route-verify', 'confirm'])
  ws.nodes.find(node => node.id === 'reverse').tunnel = 'offline'
  const reverseFailed = simulateReleaseAttempt(reverse, ws, 'retry', now)
  assert.equal(reverseFailed.state, 'failed')
  assert.equal(reverseFailed.nodes.find(node => node.nodeId === 'relay').verified, false)
  const draft = { ...reverse, state: 'draft', routeId: 'direct' }
  assert.equal(canCancel(draft), true)
  const cancelled = simulateReleaseAttempt(draft, ws, 'cancel', now)
  assert.equal(canRetry(cancelled), false)
  assert.equal(canCancel({ ...draft, nodes: [{ nodeId: 'hk', applied: true, verified: false, rollback: 'none' }] }), false)
  const staleRevision = simulateReleaseAttempt({ ...reverse, revision: 99 }, ws, 'retry', now)
  assert.equal(staleRevision.state, 'failed')
  assert.equal(staleRevision.nodes.length, 0)
  assert.equal(tailText(['abc', '你好', 'x'], 2, 8).text, '你好\nx')
  assert.ok(tailText(['你好', 'world'], 10, 4).bytes <= 4)
  console.log('Activity logic checks passed: offline rollback, immutability, cleanup references, Reverse ordering, cancellation, revision mismatch and UTF-8 tail bounds.')
} finally { await rm(temp, { recursive: true, force: true }) }
