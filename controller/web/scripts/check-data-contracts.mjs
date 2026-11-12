// Exercise inert construction, explicit transport, field mapping and failure semantics.
import assert from 'node:assert/strict'
import { readFile, writeFile, mkdir, mkdtemp, rm } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'
import ts from 'typescript'
const root = fileURLToPath(new URL('../', import.meta.url))
const base = resolve(root, 'node_modules/.cache'); await mkdir(base, { recursive: true })
const temp = await mkdtemp(resolve(base, 'b00-'))
try {
  for (const name of ['live','demo']) {
    const raw = await readFile(resolve(root, `src/data/${name}.ts`), 'utf8')
    await writeFile(resolve(temp, `${name}.mjs`), ts.transpileModule(raw, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText)
  }
  const { createLiveData } = await import(resolve(temp, 'live.mjs'))
  const { createDemoData } = await import(resolve(temp, 'demo.mjs'))
  const signal = new AbortController().signal
  const id = 'node_' + '1'.repeat(32)
  const wire = { id, name: 'Test', revision: 2, updated_at: '2026-11-12T00:00:00Z', connection_mode: 'reverse_tunnel', role: 'node', control_address: 'controller.example.invalid', proxy_address: null, control: 'online', tunnel: 'offline', verification: 'not_checked', target_dns_revision: 1, last_confirmed_at: null }
  const calls = []
  let response = { items: [wire], next_cursor: null }, status = 200
  const live = createLiveData(async (url, options) => { calls.push({ url, options }); return new Response(JSON.stringify(response), { status, headers: { 'Content-Type': 'application/json' } }) })
  assert.equal(calls.length, 0, 'construction sends no request')
  const page = await live.listNodes({}, signal)
  assert.equal(page.items[0].mode, 'reverse'); assert.equal(page.items[0].address, undefined)
  assert.equal(page.items[0].targetDnsRevision, 1); assert.equal(page.items[0].control, 'online'); assert.equal(page.items[0].tunnel, 'offline')
  assert.equal(calls[0].options.signal, signal); assert.equal(calls[0].options.redirect, 'error')
  response = { error: { code: 'REVISION_CONFLICT', current_revision: 3, fields: [{ path: '/name', code: 'INVALID_ARGUMENT' }] } }; status = 409
  await assert.rejects(live.renameNode(id, 'Rename', { expected_revision: 2, idempotency_key: 'key' }, 'csrf', signal), e => e.problem.code === 'REVISION_CONFLICT' && e.problem.current_revision === 3 && e.status === 409)
  assert.equal(calls.at(-1).options.headers['X-CSRF-Token'], 'csrf')
  assert.equal(calls.at(-1).options.credentials, 'same-origin')
  const before = calls.length
  await assert.rejects(live.getNode('../bad', signal)); assert.equal(calls.length, before)
  status = 200; response = { items: [{ ...wire, revision: 9007199254740992 }], next_cursor: null }
  await assert.rejects(live.listNodes({}, signal), e => e.message === 'INVALID_API_RESPONSE')
  response = { items: [], next_cursor: null }; assert.deepEqual((await live.listNodes({}, signal)).items, [])
  const demo = createDemoData(() => page.items)
  assert.equal(demo.mode, 'demo'); assert.equal((await demo.getNode(id, signal)).mode, 'reverse')
  await assert.rejects(demo.getTask('anything', signal), /NOT_IMPLEMENTED/)
  const aborted = new AbortController(); aborted.abort(); await assert.rejects(demo.listNodes({}, aborted.signal))
  console.log('B00 data contract checks passed: inert adapters, explicit fetch, mapping, errors, revision bounds, demo isolation')
} finally { await rm(temp, { recursive: true, force: true }) }
