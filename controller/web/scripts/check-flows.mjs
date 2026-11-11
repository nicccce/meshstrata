import assert from 'node:assert/strict'
import { readFile, writeFile, mkdir, mkdtemp, rm } from 'node:fs/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { resolve } from 'node:path'
import ts from 'typescript'

const root = fileURLToPath(new URL('../', import.meta.url))
const base = resolve(root, 'node_modules/.cache')
await mkdir(base, { recursive: true })
const temp = await mkdtemp(resolve(base, 'flows-'))
try {
  const files = { seeds: 'demo/seeds', groups: 'domain/groups', route: 'features/route-editor/logic', rules: 'features/rule-profiles/rules', subscription: 'features/subscriptions/model', egress: 'features/egress/logic' }
  for (const [name, file] of Object.entries(files)) {
    const source = await readFile(resolve(root, `src/${file}.ts`), 'utf8')
    const js = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText.replaceAll("'../../domain/groups'", "'./groups.mjs'")
    await writeFile(resolve(temp, `${name}.mjs`), js)
  }
  const get = name => import(pathToFileURL(resolve(temp, `${name}.mjs`)))
  const [{ createSeed }, { freePort, pathCost, preferredProtocol }, { targetCatalog, analyzeRules }, { buildPlan, draftFor, renderConfig }, { targetIssue, parseNetwork, overlaps }] = await Promise.all(Object.keys(files).filter(k => k !== 'groups').map(get))
  const ws = createSeed()
  const entry = ws.nodes.find(n => n.id === 'hk')
  assert.equal(preferredProtocol(entry, 'strong_regulation', false, true), 'vless')
  assert.equal(preferredProtocol(entry, 'weak_regulation', false, true), 'ss2022')
  assert.equal(preferredProtocol(entry, 'strong_regulation', false, false), 'ss2022', 'client preference applies to the first hop')
  ws.measurements.push({ ...ws.measurements[0], id: 'strong-client', kind: 'browser_http', environment: 'strong_regulation', toNodeId: 'hk', value: 45, samples: 3, expiresAt: '2030-01-01T00:00:00Z', timingBasis: 'responseStart-requestStart' })
  assert.equal(pathCost(ws, ['hk'], 'strong_regulation').total, 45)
  assert.equal(pathCost(ws, ['hk'], 'weak_regulation').total, null, 'regulation profiles do not mix first-hop samples')
  ws.measurements = ws.measurements.filter(m => m.id !== 'strong-client')
  const sub = ws.subscriptions.find(s => s.id === 'sub-active')
  const draft = draftFor(sub)
  draft.data.subscription.expiresAt = '2030-01-01T00:00:00Z'
  draft.profileId = 'work'
  const plan = buildPlan(ws, draft, sub.id)
  assert.deepEqual(plan.errors, [])
  assert.equal(new Set(plan.entries.map(p => p.listener.id)).size, plan.entries.length)
  assert.ok(plan.rules.indexOf('RULE-SET,WorkDomains,All-Proxies') < plan.rules.length - 1)
  assert.equal(plan.rules.at(-1), 'MATCH,All-Proxies')
  const config = renderConfig(ws, draft, plan)
  assert.ok(config.includes('dns-query#All-Proxies'))
  assert.ok(config.includes('dns-query#DIRECT'))
  assert.ok(!config.includes('#REJECT'))
  const rejectDraft = structuredClone(draft)
  rejectDraft.profileId = 'reject-profile'
  rejectDraft.data.subscription.grouping = { allRoutes: false, byExit: false }
  ws.profiles.push({ id: 'reject-profile', name: 'Reject', revision: 1, updatedAt: '', domainSetIds: [], rules: [{ id: 'reject', type: 'MATCH', value: '', target: 'REJECT' }] })
  assert.ok(buildPlan(ws, rejectDraft, sub.id).errors.some(e => e.key === 'dnsFallbackRequired'), 'REJECT never silently falls back to direct DNS')
  const catalog = targetCatalog(ws, sub.id)
  assert.ok(catalog.names.has(ws.routes.find(r => r.id === 'direct').name))
  assert.ok(analyzeRules(ws, `MATCH,${ws.routes.find(r => r.id === 'direct').name}`, 'All-Proxies', [], sub.id).valid)
  const route = ws.routes.find(r => r.id === 'direct')
  route.state = 'rollback_failed'
  assert.deepEqual(buildPlan(ws, draft, sub.id).errors, [], 'last published revision survives a failed edit')
  const listener = ws.listeners.find(l => l.id === route.published.entryListenerId)
  const fixed = { protocol: listener.protocol, enabled: true, preferredPort: listener.port, autoPort: false }
  assert.equal(freePort(ws, listener.nodeId, listener.port, listener.transport, listener.address, [], fixed), undefined)
  const pool = { ...fixed, autoPort: true, portPool: [{ start: 31000, end: 31001 }] }
  assert.equal(freePort(ws, listener.nodeId, listener.port, listener.transport, listener.address, [], pool), 31000)
  assert.equal(pathCost(ws, ['hk'], 'strong_regulation').total, null, 'unknown first hop is not zero')
  const target = { id: 'external', name: 'External TUN', nodeId: 'sg', type: 'interface', interfaceName: 'tun0', tcp: 'not_checked', udp: 'not_checked', updatedAt: '' }
  assert.equal(targetIssue(target, ws.listeners, []), null)
  assert.equal(targetIssue({ ...target, interfaceName: 'mihomo' }, ws.listeners, []), 'loop')
  assert.ok(overlaps(parseNetwork('192.0.2.0/24'), parseNetwork('192.0.2.4/32')))
  console.log('Flow checks passed: subscription isolation, profile targets, DomainSet/DNS output, published snapshot, fixed/automatic ports, missing measurements and external interfaces.')
} finally { await rm(temp, { recursive: true, force: true }) }
