import assert from 'node:assert/strict'
import { readFile, writeFile, mkdir, mkdtemp, rm } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'
import ts from 'typescript'
import { nextTick } from 'vue'
import { createRouter, createMemoryHistory } from 'vue-router'
// Transpile only the pure foundation modules; no server, browser or API is started.
const root = fileURLToPath(new URL('../', import.meta.url))
const base = resolve(root, 'node_modules/.cache')
await mkdir(base, { recursive: true })
const temp = await mkdtemp(resolve(base, 'f00-'))
try {
 for (const [source, output] of [['app/query', 'query'], ['app/routes', 'routes'], ['demo/seeds', 'seeds'], ['demo/draft-data', 'draft-data'], ['demo/workspace', 'workspace']]) {
  const raw = await readFile(resolve(root, `src/${source}.ts`), 'utf8')
  const js = ts.transpileModule(raw, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText.replace("from './seeds'", "from './seeds.mjs'").replace("from './draft-data'", "from './draft-data.mjs'")
  await writeFile(resolve(temp, `${output}.mjs`), js)
 }
 const { sanitizeQuery, createSafeReturnTo } = await import(resolve(temp, 'query.mjs'))
 const { pageRoutes } = await import(resolve(temp, 'routes.mjs'))
 const { createSeed } = await import(resolve(temp, 'seeds.mjs'))
 const memory = new Map()
 globalThis.localStorage = { getItem: key => memory.get(key) ?? null, setItem: (key, value) => memory.set(key, value) }
 const { decodeWorkspace, safeJson, commands, workspace, storageKey, sessionSecrets } = await import(resolve(temp, 'workspace.mjs'))
 const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', redirect: '/overview' }, ...pageRoutes.map(({ component, ...r }) => ({ ...r, component: {} })), { path: '/:pathMatch(.*)*', name: 'not-found', component: {} }] })
 const safeReturnTo = createSafeReturnTo(router.resolve)
 assert.deepEqual(sanitizeQuery('node-new', Object.fromEntries([['constructor', 'x'], ['__proto__', 'x'], ['toString', 'x']])), {})
 assert.deepEqual(sanitizeQuery('node-detail', { tab: 'dns', password: 'redacted', page: '0', panel: 'admin' }), { tab: 'dns' })
 assert.deepEqual(sanitizeQuery('route-new', { step: 'review', exitNodeId: 'hk', unknown: 'x' }), { step: 'review', exitNodeId: 'hk' })
 assert.deepEqual(sanitizeQuery('nodes', { page: '2', sort: '-name', q: 'HK', token: 'redacted', status: ['online'] }), { page: '2', sort: '-name', q: 'HK' })
 for (const bad of ['https://example.invalid', '//example.invalid', '/login', '/unknown', '/\\evil', '/nodes#secret', '/nodes\n']) assert.equal(safeReturnTo(bad), undefined, bad)
 assert.equal(safeReturnTo('/nodes/hk?tab=dns&token=x'), '/nodes/hk?tab=dns')
 for (const r of pageRoutes) { const path = r.path.replace(/:[A-Za-z]+/g, 'demo-id'); assert.equal(router.resolve(path).name, r.name, path); await router.push(path); assert.equal(router.currentRoute.value.name, r.name) }
 await router.push('/overview'); await router.push('/nodes'); router.back(); await new Promise(r => setTimeout(r, 0)); assert.equal(router.currentRoute.value.name, 'overview')
 assert.equal(router.resolve('/does-not-exist').name, 'not-found')
 for (const scenario of ['normal', 'empty', 'error', 'stale', 'offline', 'partial']) {
  const seed = createSeed(scenario); assert.deepEqual(decodeWorkspace(safeJson(seed)), JSON.parse(safeJson(seed)))
  for (const route of seed.routes) {
   for (const id of route.draft.nodeIds) assert.ok(seed.nodes.some(n => n.id === id))
   assert.ok(seed.listeners.some(l => l.id === route.draft.entryListenerId))
   for (const link of route.draft.links) { assert.ok(seed.listeners.some(l => l.id === link.listenerId)); if (link.tunnelId) assert.ok(seed.tunnels.some(t => t.id === link.tunnelId)) }
   if (route.draft.policyId) assert.ok(seed.policies.some(p => p.id === route.draft.policyId && p.revision === route.draft.policyRevision))
  }
  for (const sub of seed.subscriptions) { assert.ok(seed.profiles.some(p => p.id === sub.profileId)); for (const id of sub.entryIds) assert.ok(seed.entries.some(e => e.id === id && e.subscriptionId === sub.id)) }
 }
 assert.throws(() => decodeWorkspace('{bad'))
 assert.throws(() => decodeWorkspace('{"schemaVersion":99}'))
 const broken = createSeed(); broken.routes[0].draft.nodeIds = null; assert.throws(() => decodeWorkspace(safeJson(broken)))
 const variants = createSeed()
 variants.targets.push({ id: 'interface-target', name: 'WG', updatedAt: '', nodeId: 'sg', type: 'interface', interfaceName: 'wg0', tcp: 'not_checked', udp: 'not_checked' })
 variants.releases.push({ id: 'revoke-sub', name: 'Revoke', updatedAt: '', kind: 'revoke', subscriptionId: 'sub-active', state: 'verifying', revision: 1, nodes: [], cleanup: 'pending' })
 assert.doesNotThrow(() => decodeWorkspace(safeJson(variants)))
 const missingRoute = createSeed(); delete missingRoute.entries[0].routeId; assert.throws(() => decodeWorkspace(safeJson(missingRoute)))
 commands.saveDraft({ id: 'test', feature: 'test', name: 'Non-sensitive draft', nodeIds: ['hk'], routeIds: ['direct'], updatedAt: '', password: 'never store', nested: { token: 'never store' } })
 await nextTick()
 const restored = decodeWorkspace(memory.get(storageKey)); assert.equal(restored.drafts[0].name, 'Non-sensitive draft'); assert.ok(!memory.get(storageKey).includes('never store'))
 commands.saveDraft({ id: 'full-form', feature: 'subscriptions', name: 'Draft with form data', nodeIds: [], routeIds: ['direct'], updatedAt: '', data: { subscription: { description: 'My laptop', expiresAt: '2027-01-01', grouping: { allRoutes: true, byExit: true }, client: { mode: 'tun', ipv6: false, allowBusinessUdp: false }, selections: [], unlisted: 'must not survive' }, profile: { description: '', text: 'MATCH,All-Proxies', fallback: 'All-Proxies', domainSetIds: [], secret: 'must not survive' }, arbitrary: { nested: 'must not survive' } } })
 await nextTick()
 const form = decodeWorkspace(memory.get(storageKey)).drafts.find(d => d.id === 'full-form')
 assert.equal(form.data.subscription.description, 'My laptop')
 assert.equal(form.data.profile.text, 'MATCH,All-Proxies')
 assert.ok(!memory.get(storageKey).includes('must not survive'))
 commands.upsert('nodes', { ...createSeed().nodes[1], name: 'Renamed' }); assert.equal(workspace.nodes.find(n => n.id === 'hk').name, 'Renamed')
 sessionSecrets.set('token', 'in memory'); commands.reset('offline'); assert.equal(sessionSecrets.size, 0); assert.ok(workspace.nodes.every(n => n.control === 'offline')); assert.equal(workspace.drafts.length, 0)
 console.log(`Foundation checks passed: ${pageRoutes.length} routes, query/returnTo, memory history, six scenarios, references, persistence, draft projection, reset.`)
} finally { await rm(temp, { recursive: true, force: true }) }
