<script setup lang="ts">
import { computed, ref, watch, type DeepReadonly } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NFormItem, NInput, NInputNumber, NSelect, NTag, useMessage } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import DemoNotice from '../../components/DemoNotice.vue'
import RoutePath from '../../components/RoutePath.vue'
import EgressPolicyDrawer from '../egress/EgressPolicyDrawer.vue'
import { entityLocation, pageLocation } from '../../app/router'
import { useWorkspace } from '../../demo/workspace'
import { useDraft } from '../../composables/useDraft'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import { useFormat } from '../../composables/useFormat'
import type { Draft, EgressPolicy, Listener, Node, Protocol, Release, Route, RouteRevision } from '../../domain'
import { candidates, copy, freePort, listenerIds, now, pathCost, pathIssues, preferredProtocol, protocols, uid, type Candidate, type MeasurementMode, type PathDraft, type RouteForm } from './logic'

const props = defineProps<{ routeId?: string; draftId?: string }>()
const { t } = useI18n(); const tr = (key: string, params: Record<string, string | number> = {}) => t(`features.route-editor.${key}`, params)
const { workspace, commands, persistenceIssue, localClientPaired } = useWorkspace(); const route = useRoute(); const router = useRouter(); const message = useMessage(); const format = useFormat()
const source = workspace.routes.find(r => r.id === props.routeId)
const initialExit = source?.draft.nodeIds.at(-1) ?? (typeof route.query.exitNodeId === 'string' ? route.query.exitNodeId : '')
const initialForm: RouteForm = { exitNodeId: initialExit, environment: source?.draft.environment ?? workspace.settings.environment ?? 'mainland_china', allowUdpTransport: source?.draft.allowUdpTransport ?? workspace.settings.allowUdpTransport ?? false, requireUdpRelay: source?.draft.requireUdpRelay ?? workspace.settings.requireUdpRelay ?? false, selectedPaths: source ? [{ id: source.id, name: source.name, revision: { ...copy<RouteRevision>(source.draft), revision: (source.published?.revision ?? source.draft.revision) + 1, validated: false } }] : [], currentPathId: source?.id }
const id = props.draftId ?? (props.routeId ? `route-editor-${props.routeId}` : uid('route-draft'))
const initial: Draft = { id, feature: 'route-editor', objectId: props.routeId, name: source?.name ?? tr('newDraft'), nodeIds: initialExit ? [initialExit] : [], routeIds: props.routeId ? [props.routeId] : [], step: String(route.query.step ?? 'exit'), updatedAt: now(), data: { route: initialForm } }
const draft = useDraft(id, initial)
if (!draft.value.value.data?.route) { draft.value.value.data = { route: initialForm } }
const form = computed(() => draft.value.value.data!.route!)
const paths = computed(() => form.value.selectedPaths)
paths.value.forEach(p => { p.revision.validated = false })
const exit = computed(() => workspace.nodes.find(n => n.id === form.value.exitNodeId && !n.removedAt))
const isReverse = computed(() => exit.value?.mode === 'reverse')
const exitOptions = computed(() => workspace.nodes.filter(n => !n.removedAt).map(n => ({ label: `${n.name} · ${n.location} · ${n.mode === 'reverse' ? tr('reverse') : tr('direct')} · ${t(`status.${n.control}`)}`, value: n.id, disabled: n.control !== 'online' })))
const count = ref<number | null>(3); const recommended = ref(false); const search = ref(''); const mode = ref<MeasurementMode>('browser'); const measurementFailure = ref(false)
const candidateList = computed(() => recommended.value ? candidates(workspace, form.value, count.value ?? 3).filter(c => c.nodeIds.some(id => nodeName(id).toLowerCase().includes(search.value.toLowerCase()))) : [])
const activePath = computed(() => paths.value.find(p => p.id === form.value.currentPathId) ?? paths.value[0])
const stepKeys = ['exit', 'paths', 'options', 'review'] as const
if (typeof route.query.step === 'string' && stepKeys.includes(route.query.step as typeof stepKeys[number])) draft.value.value.step = route.query.step
const step = computed(() => Math.max(0, stepKeys.indexOf(draft.value.value.step as typeof stepKeys[number])))
const pathErrors = (path: PathDraft) => pathIssues(workspace, form.value, path, props.routeId)
const exitIssues = computed(() => !exit.value ? ['chooseExit'] : exit.value.control !== 'online' ? ['nodeOffline'] : isReverse.value && form.value.requireUdpRelay ? ['reverseInvalid'] : isReverse.value && !workspace.nodes.some(n => n.id === workspace.settings.relayNodeId && n.role === 'both' && n.control === 'online' && !n.removedAt) ? ['relayMissing'] : isReverse.value && workspace.settings.relay?.enabled === false ? ['relayMissing'] : [])
const allIssues = computed(() => [...exitIssues.value, ...(!paths.value.length ? ['selectPath'] : []), ...paths.value.flatMap(pathErrors)])
const currentIssues = computed(() => step.value === 0 ? exitIssues.value : step.value === 1 ? [...exitIssues.value, ...(!paths.value.length ? ['selectPath'] : []), ...paths.value.flatMap(p => pathErrors(p).filter(e => !['nameRequired', 'nameConflict', 'nameLength'].includes(e)))] : allIssues.value)
const issueShown = ref(false); const busy = ref(false); const phase = ref<'idle' | 'validation' | 'applied'>('idle')
onBeforeRouteLeave(() => { if (busy.value && phase.value === 'applied') { message.info(tr('applyingNotice')); return false } })
const measureTask = useSimulatedTask(); const publishTask = useSimulatedTask(); const drawer = ref(route.query.panel === 'egress')
const results = ref<{ routeId: string; releaseId: string; name: string; state: Release['state'] }[]>([])
const outcome = ref<'scenario' | 'success' | 'failure' | 'rollback_pending' | 'rollback_failed' | 'partial'>('scenario')
const manualNodes = ref<string[]>([]); const portInputs = ref<Record<string, number | null>>({})
const protocolChoices = ref<Record<string, Protocol>>({})
const revisionForCost = (nodeIds: readonly string[]) => {
  const selected = paths.value.find(p => p.revision.nodeIds.join('>') === nodeIds.join('>'))
  const transports = nodeIds.map((_, i) => (selected && selectedListener(selected, i)?.protocol === 'hysteria2') || (i === 0 && protocolChoices.value[`candidate-${nodeIds.join('-')}`] === 'hysteria2') ? 'udp' : 'tcp')
  return pathCost(workspace, nodeIds, form.value.environment, mode.value === 'network' ? 'network' : 'browser', transports)
}
const groups = computed(() => (['network', 'browser', 'partial'] as const).map(key => ({ key, items: candidateList.value.filter(c => revisionForCost(c.nodeIds).mode === key) })))
const selectedIds = computed(() => paths.value.map(p => p.revision.nodeIds.join('>')))
const manualOptions = computed(() => workspace.nodes.filter(n => n.mode === 'direct' && n.control === 'online' && !n.removedAt && n.id !== form.value.exitNodeId).map(n => ({ label: n.name, value: n.id })))
const policyOptions = computed(() => [{ label: tr('nodeDefault'), value: '' }, ...workspace.policies.filter(p => p.terminalNodeId === form.value.exitNodeId).map(p => ({ label: `${p.name} · r${p.revision} · ${tr('references', { count: workspace.routes.filter(r => r.published?.policyId === p.id).length })}`, value: p.id }))])
function nodeName(id: string) { return workspace.nodes.find(n => n.id === id)?.name ?? `${tr('missing')} (${id})` }
function pathNodes(ids: readonly string[]) { return ids.map(id => ({ id, name: nodeName(id) })) }
function invalidate() { paths.value.forEach(p => { p.revision.validated = false }) }
const formSignature = computed(() => JSON.stringify({ exit: form.value.exitNodeId, environment: form.value.environment, allow: form.value.allowUdpTransport, require: form.value.requireUdpRelay, paths: paths.value.map(p => ({ ...p, revision: { ...p.revision, validated: false } })) }))
watch(formSignature, () => { if (!busy.value) { invalidate(); results.value = [] } }, { flush: 'sync' })
const resourceSignature = computed(() => JSON.stringify(paths.value.map(p => ({ nodes: workspace.nodes.filter(n => p.revision.nodeIds.includes(n.id)), listeners: workspace.listeners.filter(l => listenerIds(p.revision).includes(l.id)), policy: workspace.policies.find(policy => policy.id === p.revision.policyId), targets: workspace.targets.filter(target => workspace.policies.find(policy => policy.id === p.revision.policyId)?.rules.some(rule => rule.targetId === target.id)), tunnels: workspace.tunnels.filter(tunnel => p.revision.links.some(link => link.tunnelId === tunnel.id)) }))))
watch(resourceSignature, () => { if (!busy.value) invalidate() }, { flush: 'sync' })
watch(() => route.query.step, value => { if (typeof value === 'string' && stepKeys.includes(value as typeof stepKeys[number])) draft.value.value.step = value })
watch(() => route.query.panel, value => { drawer.value = value === 'egress' })
watch(drawer, value => { if ((route.query.panel === 'egress') !== value) void router.replace(location(stepKeys[step.value]!, value ? 'egress' : undefined)) })
function location(nextStep: string, panel?: string) { return pageLocation(props.routeId ? 'route-edit' : 'route-new', props.routeId ? { routeId: props.routeId } : {}, { step: nextStep, ...(props.draftId ? { draftId: props.draftId } : {}), ...(!props.routeId && route.query.exitNodeId ? { exitNodeId: route.query.exitNodeId } : {}), ...(panel ? { panel } : {}) }) }
function go(index: number) {
  if (busy.value || index < 0 || index > 3) return
  measureTask.cancel()
  if (index > step.value && currentIssues.value.length) { issueShown.value = true; return }
  if (index >= 2 && (!paths.value.length || exitIssues.value.length)) { issueShown.value = true; return }
  draft.value.value.step = stepKeys[index]; issueShown.value = false
  void router.push(location(stepKeys[index]!))
}
function changeExit(value: string) { form.value.exitNodeId = value; form.value.selectedPaths = []; form.value.currentPathId = undefined; recommended.value = false; if (workspace.nodes.find(n => n.id === value)?.mode === 'reverse') form.value.requireUdpRelay = false }
function recommend() { if (exitIssues.value.length) { issueShown.value = true; return }; recommended.value = true; search.value = ''; invalidate() }
function sourceProtocol(node: DeepReadonly<Node> | undefined, i: number) { return preferredProtocol(node, form.value.environment, form.value.allowUdpTransport, i === 0) }
function createPath(nodeIds: string[], entryProtocol?: Protocol): PathDraft | undefined {
  const pathId = props.routeId ?? uid('route')
  const staged: Listener[] = []
  for (let i = 0; i < nodeIds.length; i++) {
    const node = workspace.nodes.find(n => n.id === nodeIds[i]); const protocol = i === 0 && entryProtocol ? entryProtocol : node?.mode === 'reverse' ? 'ss2022' : sourceProtocol(node, i)
    if (!node || !protocol || !protocols(node, form.value.allowUdpTransport).includes(protocol)) { message.error(tr('errors.protocolInvalid')); return }
    const defaults = node.protocolDefaults.find(p => p.protocol === protocol)
    const address = node.mode === 'reverse' ? '127.0.0.1' : defaults?.listenAddress ?? '0.0.0.0'
    const transport: Listener['transport'] = node.mode === 'reverse' ? 'tcp' : protocol === 'hysteria2' ? 'udp' : defaults?.transport ?? (form.value.requireUdpRelay ? 'tcp_udp' : 'tcp')
    const port = freePort(workspace, node.id, defaults?.preferredPort ?? 0, transport, address, staged, defaults)
    if (!port) { message.error(tr('errors.portExhausted')); return }
    staged.push({ id: uid('listener-draft'), name: tr('listenerName', { node: node.name }), updatedAt: now(), nodeId: node.id, protocol, address, port, transport, routeIds: [], revision: 1, state: 'draft', cleanup: 'none' })
  }
  staged.forEach((l, i) => { l.nextHopListenerId = staged[i + 1]?.id })
  const revision: RouteRevision = { revision: (source?.published?.revision ?? source?.draft.revision ?? 0) + 1, nodeIds, entryListenerId: staged[0]!.id, links: staged.slice(1).map((l, i) => ({ fromNodeId: nodeIds[i]!, toNodeId: l.nodeId, listenerId: l.id, protocol: l.protocol })), validated: false, environment: form.value.environment, allowUdpTransport: form.value.allowUdpTransport, requireUdpRelay: form.value.requireUdpRelay }
  if (workspace.nodes.find(n => n.id === nodeIds.at(-1))?.mode === 'reverse') {
    const pool = workspace.settings.relay?.mappingPool ?? { start: 23000, end: 23999 }
    let mappingPort: number | undefined
    for (let port = Math.max(1, pool.start); port <= Math.min(pool.end, 65535); port++) if (!workspace.tunnels.some(t => t.relayNodeId === nodeIds[0] && t.mappingPort === port) && !workspace.listeners.some(l => l.nodeId === nodeIds[0] && l.port === port && l.transport !== 'udp') && !staged.some(l => l.nodeId === nodeIds[0] && l.port === port)) { mappingPort = port; break }
    if (!mappingPort) { message.error(tr('errors.portExhausted')); return }
    const tunnelId = uid('tunnel-draft')
    commands.upsert('tunnels', { id: tunnelId, name: tr('tunnelName'), updatedAt: now(), relayNodeId: nodeIds[0]!, remoteNodeId: nodeIds[1]!, targetListenerId: staged[1]!.id, relayEndpoint: workspace.settings.relay?.endpoint ?? 'relay.example.invalid:9444', mappingPort, transport: 'tcp_tls', revision: 1, routeIds: [], relayApplied: false, remoteApplied: false })
    revision.links[0]!.tunnelId = tunnelId; revision.links[0]!.tunnelRevision = 1
  }
  staged.forEach(l => commands.upsert('listeners', l))
  return { id: pathId, name: props.routeId ? source?.name ?? '' : tr('suggestedName', { exit: exit.value?.name ?? '', count: paths.value.length + 1 }), revision }
}
function selectCandidate(candidate: Candidate, protocol?: Protocol) {
  const existing = paths.value.find(p => p.revision.nodeIds.join('>') === candidate.nodeIds.join('>'))
  if (existing && !protocol) { form.value.selectedPaths = paths.value.filter(p => p.id !== existing.id); return }
  const created = createPath(candidate.nodeIds, protocol ?? protocolChoices.value[candidate.id]); if (!created) return
  if (existing) { created.id = existing.id; created.name = existing.name; form.value.selectedPaths = paths.value.map(p => p.id === existing.id ? created : p) }
  else form.value.selectedPaths = props.routeId ? [created] : [...paths.value, created]
  form.value.currentPathId = created.id
}
function addManual() {
  if (!exit.value || isReverse.value) return
  if (manualNodes.value.length > 5) { message.error(tr('errors.pathInvalid')); return }
  const nodes = [...manualNodes.value, exit.value.id]
  if (selectedIds.value.includes(nodes.join('>'))) { message.warning(tr('alreadySelected')); return }
  const created = createPath(nodes); if (created) { form.value.selectedPaths = props.routeId ? [created] : [...paths.value, created]; form.value.currentPathId = created.id }
}
function syncDraft() {
  draft.value.value.name = paths.value[0]?.name ?? tr('newDraft'); draft.value.value.nodeIds = [...new Set(paths.value.flatMap(p => p.revision.nodeIds))]; draft.value.value.updatedAt = now()
  paths.value.forEach(p => { p.revision.environment = form.value.environment; p.revision.allowUdpTransport = form.value.allowUdpTransport; p.revision.requireUdpRelay = form.value.requireUdpRelay })
}
function saveDraft() {
  syncDraft(); draft.save()
  if (persistenceIssue.value) message.warning(tr('storageFailed')); else message.success(tr('saved'))
  void router.replace(pageLocation(props.routeId ? 'route-edit' : 'route-new', props.routeId ? { routeId: props.routeId } : {}, { step: stepKeys[step.value], draftId: id, ...(drawer.value ? { panel: 'egress' } : {}) }))
}
function discard() { draft.discard(); issueShown.value = false; recommended.value = false; void router.replace(location(draft.value.value.step ?? 'exit')) }
function isolatePolicy(path: PathDraft) {
  if (!workspace.routes.some(r => r.published && listenerIds(r.published).some(id => listenerIds(path.revision).includes(id)))) return true
  const privatePath = createPath([...path.revision.nodeIds], selectedListener(path, 0)?.protocol)
  if (!privatePath) return false
  path.revision = privatePath.revision
  return true
}
function setPolicy(policyId: string) { const path = activePath.value; if (!path || !isolatePolicy(path)) return; const policy = workspace.policies.find(p => p.id === policyId); path.revision.policyId = policy?.id; path.revision.policyRevision = policy?.revision; path.revision.validated = false }
function savePolicy(policy: EgressPolicy) {
  const path = activePath.value; if (!path || policy.terminalNodeId !== form.value.exitNodeId) { message.error(tr('errors.policyInvalid')); return }
  if (policy.rules.length === 1 && policy.rules[0]?.destination === 'MATCH' && workspace.targets.find(t => t.id === policy.rules[0]?.targetId)?.type === 'node_default') {
    setPolicy(''); drawer.value = false; return
  }
  // Never overwrite a stable policy revision referenced by a published route.
  const protectedId = workspace.routes.some(r => r.published?.policyId === policy.id) || workspace.policies.some(p => p.id === policy.id)
  const edited = copy<EgressPolicy>(policy); if (protectedId) { edited.id = uid('policy-draft'); edited.revision = 1 }
  if (!isolatePolicy(path)) return
  edited.updatedAt = now(); commands.upsert('policies', edited); path.revision.policyId = edited.id; path.revision.policyRevision = edited.revision; invalidate(); drawer.value = false; message.success(tr('policySaved'))
}
function selectedListener(path: PathDraft, index: number) { return workspace.listeners.find(l => l.id === listenerIds(path.revision)[index]) }
function listenerOptions(path: PathDraft, index: number) { return workspace.listeners.filter(l => l.nodeId === path.revision.nodeIds[index] && !l.entryIds?.length).map(l => ({ label: `${l.name} · ${l.protocol} · ${l.address}:${l.port} · ${l.transport}`, value: l.id })) }
function changeListener(path: PathDraft, index: number, listenerId: string) {
  const listener = workspace.listeners.find(l => l.id === listenerId); if (!listener) return
  if (index === 0) path.revision.entryListenerId = listener.id
  else { const link = path.revision.links[index - 1]!; link.listenerId = listener.id; link.protocol = listener.protocol }
  path.revision.validated = false
}
function allocateListener(path: PathDraft, index: number, protocol?: Protocol) {
  const current = selectedListener(path, index); const node = workspace.nodes.find(n => n.id === path.revision.nodeIds[index]); if (!current || !node) return
  if (isReverse.value && index > 0) { message.warning(tr('reverseLocked')); return }
  const edited = copy<Listener>(current); edited.id = uid('listener-draft'); edited.revision = current.revision + 1; edited.protocol = protocol ?? current.protocol; edited.routeIds = []; edited.entryIds = []; edited.state = 'draft'; edited.updatedAt = now(); edited.nextHopListenerId = path.revision.links[index]?.listenerId
  edited.transport = edited.protocol === 'hysteria2' ? 'udp' : form.value.requireUdpRelay ? 'tcp_udp' : 'tcp'
  const requested = portInputs.value[`${path.id}-${index}`]
  if (requested !== undefined && requested !== null) {
    if (!Number.isInteger(requested) || requested < 1 || requested > 65535) { message.error(tr('errors.portInvalid')); return }
    if (freePort(workspace, node.id, requested, edited.transport, edited.address) !== requested) { message.error(tr('errors.portConflict')); return }
    edited.port = requested
  } else {
    const defaults = node.protocolDefaults.find(p => p.protocol === edited.protocol)
    const port = freePort(workspace, node.id, defaults?.preferredPort ?? 0, edited.transport, edited.address, [], defaults)
    if (!port) { message.error(tr('errors.portExhausted')); return }; edited.port = port
  }
  commands.upsert('listeners', edited); changeListener(path, index, edited.id)
  // The upstream private listener must be rebound without touching published resources.
  for (let parentIndex = index - 1; parentIndex >= 0; parentIndex--) {
    const upstream = selectedListener(path, parentIndex)
    if (upstream) {
      const parent = copy<Listener>(upstream); const port = freePort(workspace, parent.nodeId, 0, parent.transport, parent.address)
      if (port) { parent.id = uid('listener-draft'); parent.port = port; parent.nextHopListenerId = path.revision.links[parentIndex]?.listenerId; parent.routeIds = []; parent.entryIds = []; parent.state = 'draft'; parent.updatedAt = now(); commands.upsert('listeners', parent); changeListener(path, parentIndex, parent.id) }
    }
  }
  portInputs.value[`${path.id}-${index}`] = null
}
async function measure(onlyMissing: boolean) {
  if (measureTask.state.value === 'running' || !candidateList.value.length) return
  measurementFailure.value = false
  const requested = candidateList.value.map(c => [...c.nodeIds]); const environment = form.value.environment; const desired = mode.value; const scenario = workspace.scenario
  const completed = await measureTask.run(scenario === 'error' ? 'error' : 'success', 650)
  if (!completed) { measurementFailure.value = measureTask.state.value === 'error'; return }
  const seen = new Set<string>(); const startedAt = now()
  requested.forEach(nodeIds => nodeIds.forEach((toNodeId, index) => {
    const fromNodeId = index ? nodeIds[index - 1] : undefined
    const key = `${fromNodeId ?? 'client'}-${toNodeId}-${desired}`; if (seen.has(key)) return; seen.add(key)
    if (index && workspace.nodes.find(n => n.id === toNodeId)?.mode === 'reverse') return // mapping connect time is never tunnel RTT
    const current = pathCost(workspace, nodeIds, environment, desired === 'network' ? 'network' : 'browser').segments[index]
    if (onlyMissing && current && current.value !== null && Date.parse(current.expiresAt) > Date.now()) return
    const partial = desired === 'partial' || scenario === 'partial'
    const firstAvailable = desired === 'network' ? localClientPaired.value && !!workspace.settings.localClient?.rawRtt : !!workspace.nodes.find(n => n.id === toNodeId)?.probe?.enabled
    const value = !index && (partial || !firstAvailable) ? null : 18 + [...key].reduce((n, char) => n + char.charCodeAt(0), 0) % 85
    commands.upsert('measurements', { id: uid('measurement-demo'), name: tr('simulatedMeasurement'), updatedAt: startedAt, routeId: props.routeId ?? '', kind: index || desired === 'network' ? 'network_rtt' : 'browser_http', method: index || desired === 'network' ? 'simulated_tcp_rtt' : 'simulated_resource_timing', transport: index || desired === 'network' ? 'tcp' : 'h2', value, samples: value === null ? 0 : 3, environment, startedAt, observedAt: startedAt, expiresAt: scenario === 'stale' ? '2020-01-01T00:00:00Z' : new Date(Date.now() + (workspace.settings.measurementTtlHours ?? 96) * 3600000).toISOString(), scope: index ? 'node-to-node' : 'client-to-entry', revision: 1, fromNodeId, toNodeId, timingBasis: index || desired === 'network' ? 'tcp_connect' : 'responseStart-requestStart', dispersion: value === null ? undefined : 4, failedSamples: value === null ? 3 : 0, pathVerified: false })
  }))
  invalidate(); message.info(tr('measurementDone'))
}
function networkChanged() { workspace.measurements.filter(m => !m.fromNodeId && m.toNodeId).forEach(m => commands.upsert('measurements', { ...copy(m), expiresAt: '2020-01-01T00:00:00Z' })); invalidate(); message.info(tr('networkExpired')) }
function validate() { syncDraft(); issueShown.value = true; if (allIssues.value.length) { message.error(tr('validationFailed')); return false }; paths.value.forEach(p => { p.revision.validated = true }); message.success(tr('validationPassed')); return true }
const affectedNodes = computed(() => [...new Set(paths.value.flatMap(p => p.revision.nodeIds))])
const affectedSubscriptions = computed(() => workspace.subscriptions.filter(s => props.routeId && s.routeIds.includes(props.routeId)))
function stageNames(reverse: boolean) { return reverse ? ['parameters', 'remoteListener', 'relayAuthorization', 'tunnelMapping', 'mappingAuthentication', 'entryApply', 'listenersAuthentication', 'endToEnd'] : ['parameters', 'downstreamApply', 'entryApply', 'listenersAuthentication', 'endToEnd'] }
function releaseOutcome(index: number) {
  const selected = outcome.value === 'scenario' ? workspace.scenario === 'error' ? 'failure' : workspace.scenario === 'partial' ? 'partial' : workspace.scenario === 'offline' ? 'rollback_pending' : 'success' : outcome.value
  return selected === 'partial' ? index === 0 ? 'success' : 'failure' : selected
}
async function publish() {
  if (busy.value || measureTask.state.value === 'running') return
  if (!paths.value.every(p => p.revision.validated) || !validate()) { if (!paths.value.every(p => p.revision.validated)) message.warning(tr('validateFirst')); return }
  syncDraft(); draft.save(); busy.value = true; phase.value = 'validation'; results.value = []
  const batch = paths.value.map(p => copy<PathDraft>(p))
  for (let index = 0; index < batch.length; index++) {
    const path = batch[index]!; const existing = workspace.routes.find(r => r.id === (props.routeId ?? path.id)); const routeId = props.routeId ?? path.id
    const revision = copy<RouteRevision>(path.revision); revision.revision = Math.max(existing?.published?.revision ?? 0, existing?.draft.revision ?? 0, revision.revision - 1) + 1
    const routeRecord: Route = { id: routeId, name: path.name.trim(), updatedAt: now(), state: 'validating', draft: revision, ...(props.routeId && existing?.published ? { published: copy<RouteRevision>(existing.published) } : {}), verification: props.routeId ? existing?.verification ?? 'not_checked' : 'not_checked', cleanup: 'none', ...(existing?.verifiedAt ? { verifiedAt: existing.verifiedAt } : {}) }
    commands.upsert('routes', routeRecord)
    const releaseId = uid('release'); const reverse = workspace.nodes.find(n => n.id === revision.nodeIds.at(-1))?.mode === 'reverse'
    const release: Release = { id: releaseId, name: tr('publishName', { name: path.name }), updatedAt: now(), kind: 'publish', routeId, state: 'validating', revision: revision.revision, sourceDraftId: id, startedAt: now(), nodes: revision.nodeIds.map(nodeId => ({ nodeId, applied: false, verified: false, rollback: 'none' })), stages: stageNames(reverse).map((name, i) => ({ id: `${releaseId}-${i}`, name: tr(`stages.${name}`), state: i === 0 ? 'running' : 'pending' })), cleanup: 'none' }
    commands.upsert('releases', release); phase.value = 'validation'
    const preflight = await publishTask.run('success', 400)
    if (!preflight) { release.state = 'failed'; release.issue = { code: 'features.route-editor.errors.cancelled' }; release.stages![0]!.state = 'not_checked'; routeRecord.state = 'draft'; routeRecord.draft.validated = false }
    else {
      phase.value = 'applied'; release.state = 'verifying'; routeRecord.state = 'verifying'
      release.nodes.forEach(n => { n.applied = workspace.nodes.find(node => node.id === n.nodeId)?.control === 'online' })
      release.stages!.forEach((stage, i) => { stage.state = i < release.stages!.length - 2 ? 'succeeded' : 'running'; if (stage.state === 'succeeded') stage.completedAt = now() })
      commands.upsert('releases', release); commands.upsert('routes', routeRecord)
      const intended = releaseOutcome(index); const success = await publishTask.run(intended === 'success' ? 'success' : 'error', 650)
      if (success) {
        release.state = 'published'; release.nodes.forEach(n => { n.verified = n.applied }); release.stages!.forEach(s => { s.state = 'succeeded'; s.completedAt = now() })
        routeRecord.state = 'published'; routeRecord.published = copy(revision); routeRecord.published.validated = true; routeRecord.draft.validated = true; routeRecord.verification = 'verified'; routeRecord.verifiedAt = now()
      } else {
        release.state = intended === 'rollback_pending' ? 'rollback_pending' : intended === 'rollback_failed' ? 'rollback_failed' : 'rolled_back'; release.cleanup = 'pending'
        release.issue = { code: 'features.route-editor.errors.simulatedPublishFailure' }
        release.nodes.forEach((n, i) => { n.verified = false; n.rollback = !n.applied || (release.state === 'rollback_pending' && i === release.nodes.length - 1) ? 'pending' : release.state === 'rollback_failed' && i === release.nodes.length - 1 ? 'failed' : 'confirmed'; if (i === release.nodes.length - 1) n.issue = release.issue })
        const firstRunning = release.stages!.find(s => s.state === 'running'); if (firstRunning) { firstRunning.state = 'failed'; firstRunning.issue = release.issue; firstRunning.completedAt = now() }
        release.stages!.filter(s => s.state === 'running' || s.state === 'pending').forEach(s => { s.state = 'not_checked' })
        routeRecord.state = release.state; routeRecord.draft.validated = false; routeRecord.cleanup = 'pending'
        // Preserve the last published snapshot and its verification on a failed edit.
      }
    }
    release.completedAt = now(); release.updatedAt = now(); routeRecord.updatedAt = now()
    commands.upsert('releases', release); commands.upsert('routes', routeRecord)
    if (release.state === 'published') {
      listenerIds(revision).forEach(listenerId => { const listener = workspace.listeners.find(l => l.id === listenerId); if (listener) commands.upsert('listeners', { ...copy<Listener>(listener), routeIds: [...new Set([...listener.routeIds, routeId])], state: 'published' }) })
      revision.links.forEach(link => { const tunnel = workspace.tunnels.find(t => t.id === link.tunnelId); if (tunnel) commands.upsert('tunnels', { ...copy(tunnel), routeIds: [...new Set([...tunnel.routeIds, routeId])], relayApplied: true, remoteApplied: true }) })
    }
    commands.upsert('events', { id: uid('event'), name: tr('publishName', { name: path.name }), updatedAt: now(), kind: 'publish', entityType: 'route', entityId: routeId, status: release.state, releaseId })
    results.value.push({ routeId, releaseId, name: path.name, state: release.state })
    const current = paths.value.find(p => p.id === path.id); if (current) current.revision.validated = release.state === 'published'
    if (publishTask.state.value === 'cancelled') break
  }
  const publishedIds = new Set(results.value.filter(r => r.state === 'published').map(r => r.routeId))
  form.value.selectedPaths = paths.value.filter(p => !publishedIds.has(props.routeId ?? p.id))
  busy.value = false; phase.value = 'idle'; draft.value.value.updatedAt = now(); draft.save()
  if (!paths.value.length) commands.remove('drafts', id)
  if (results.value.length === 1 && batch.length === 1) void router.push(entityLocation('release', results.value[0]!.releaseId))
  else if (results.value.length) message.info(tr('batchFinished'))
}
</script>
<template>
  <div class="route-editor">
    <PageHeader :title="props.routeId ? tr('editTitle', { name: source?.name ?? '' }) : tr('RouteEditorPage.title')" :description="tr('RouteEditorPage.goal')">
      <template #actions><RouterLink :to="props.routeId ? entityLocation('route', props.routeId) : pageLocation('routes')">{{ tr('backToRoutes') }}</RouterLink></template>
    </PageHeader>
    <DemoNotice />
    <p class="muted">{{ tr('offlineNotice') }}</p>
    <NAlert v-if="persistenceIssue" type="warning">{{ tr('storageFailed') }}</NAlert>
    <nav class="wizard-steps" :aria-label="tr('wizardNavigation')">
      <button v-for="(key, index) in stepKeys" :key="key" type="button" :class="{ current: step === index }" :aria-current="step === index ? 'step' : undefined" :disabled="busy" @click="go(index)"><span>{{ index + 1 }}</span>{{ tr(`steps.${key}`) }}</button>
    </nav>
    <div class="editor-layout">
      <div class="editor-main">
        <NAlert v-if="issueShown && currentIssues.length" type="error" :title="tr('fixIssues')" role="alert"><ul><li v-for="issue in [...new Set(currentIssues)]" :key="issue">{{ tr(`errors.${issue}`) }}</li></ul></NAlert>
        <fieldset :disabled="busy" class="editor-fields">
          <section v-if="step === 0" class="panel">
            <h2>{{ tr('steps.exit') }}</h2>
            <NFormItem :label="tr('exitNode')" label-for="exit-node" required><NSelect :value="form.exitNodeId || null" :options="exitOptions" :placeholder="tr('chooseExit')" filterable :disabled="busy" @update:value="changeExit" /></NFormItem>
            <NAlert v-if="!exitOptions.length" type="info">{{ tr('emptyNodes') }} <RouterLink :to="pageLocation('node-new')">{{ tr('addNode') }}</RouterLink></NAlert>
            <NAlert v-else-if="form.exitNodeId && !exit" type="error">{{ tr('errors.missingNode') }}</NAlert>
            <template v-if="exit">
              <div class="exit-summary"><div><strong>{{ exit.name }}</strong><p>{{ exit.location }} · {{ t(`status.${exit.control}`) }} · {{ protocols(exit, form.allowUdpTransport).join(' / ') }}</p></div><RouterLink :to="entityLocation('node', exit.id)">{{ tr('viewNode') }}</RouterLink></div>
              <NFormItem :label="tr('environment')"><NSelect v-model:value="form.environment" :disabled="busy" :options="[{ label: tr('mainland'), value: 'mainland_china' }, { label: tr('outside'), value: 'outside_mainland_china' }]" /></NFormItem>
              <div class="preference"><NCheckbox v-model:checked="form.allowUdpTransport" :disabled="busy">{{ tr('allowUdpTransport') }}</NCheckbox><p>{{ tr('allowUdpHint') }}</p></div>
              <div class="preference"><NCheckbox v-model:checked="form.requireUdpRelay" :disabled="busy || isReverse">{{ tr('requireUdpRelay') }}</NCheckbox><p>{{ tr('requireUdpHint') }}</p></div>
              <NAlert v-if="isReverse" type="warning" :title="tr('reverseFixed')">{{ tr('reverseNotice', { relay: nodeName(workspace.settings.relayNodeId) }) }} <RouterLink :to="pageLocation('settings', {}, { section: 'relay' })">{{ tr('relaySettings') }}</RouterLink></NAlert>
              <p class="muted">{{ tr('defaultPreferences') }}</p>
            </template>
          </section>
          <section v-if="step === 1" class="panel">
            <h2>{{ tr('steps.paths') }}</h2>
            <div class="recommend-controls">
              <NFormItem :label="tr('candidateCount')"><NInputNumber v-model:value="count" :min="1" :max="8" :precision="0" :disabled="busy || isReverse" /></NFormItem>
              <NButton type="primary" :disabled="busy" @click="recommend">{{ tr('recommend') }}</NButton>
            </div>
            <NAlert type="info">{{ tr('recommendHint') }}</NAlert>
            <template v-if="recommended">
              <div class="measure-controls">
                <NFormItem :label="tr('measurementMode')"><NSelect v-model:value="mode" :disabled="measureTask.state.value === 'running'" :options="[{ label: tr('modes.network'), value: 'network' }, { label: tr('modes.browser'), value: 'browser' }, { label: tr('modes.partial'), value: 'partial' }]" /></NFormItem>
                <div class="actions"><NButton :loading="measureTask.state.value === 'running'" :disabled="!candidateList.length || busy" @click="measure(true)">{{ tr('measureMissing') }}</NButton><NButton :disabled="measureTask.state.value === 'running' || !candidateList.length || busy" @click="measure(false)">{{ tr('remeasure') }}</NButton><NButton v-if="measureTask.state.value === 'running'" @click="measureTask.cancel">{{ tr('cancelMeasurement') }}</NButton><NButton :disabled="measureTask.state.value === 'running'" @click="networkChanged">{{ tr('networkChanged') }}</NButton></div>
              </div>
              <p class="muted">{{ tr('browserLimit') }} <RouterLink :to="pageLocation('settings', {}, { section: 'client' })">{{ tr('localClient') }}</RouterLink></p>
              <NAlert v-if="measurementFailure" type="error" role="alert">{{ tr('measurementFailed') }}</NAlert>
              <NAlert v-if="measureTask.state.value === 'cancelled'" type="info">{{ tr('measurementCancelled') }}</NAlert>
              <NInput v-model:value="search" :placeholder="tr('searchCandidates')" :input-props="{ 'aria-label': tr('searchCandidates') }" clearable />
              <div v-if="!candidateList.length" class="empty"><p>{{ search ? tr('noSearchMatches') : tr('noCandidates') }}</p><NButton v-if="search" @click="search = ''">{{ tr('clearSearch') }}</NButton><RouterLink v-else :to="pageLocation('nodes')">{{ tr('reviewNodes') }}</RouterLink></div>
              <section v-for="group in groups" v-show="group.items.length" :key="group.key" class="measurement-group">
                <h3>{{ tr(`modes.${group.key}`) }} <small>{{ group.items.length }}</small></h3>
                <p class="muted">{{ tr(`modeHints.${group.key}`) }}</p>
                <article v-for="candidate in group.items" :key="candidate.id" class="candidate" :class="{ selected: selectedIds.includes(candidate.nodeIds.join('>')) }">
                  <div class="candidate-top"><NCheckbox :checked="selectedIds.includes(candidate.nodeIds.join('>'))" :disabled="busy" @update:checked="selectCandidate(candidate)">{{ tr('selectCandidate') }}</NCheckbox><NTag size="small" :bordered="false">{{ tr('pathUnverified') }}</NTag></div>
                  <RoutePath :nodes="pathNodes(candidate.nodeIds)" />
                  <div class="cost-row"><strong>{{ revisionForCost(candidate.nodeIds).total === null ? tr('incomplete') : tr(group.key === 'network' ? 'networkCost' : 'estimatedCost', { value: format.number(revisionForCost(candidate.nodeIds).total) }) }}</strong><span>{{ tr('udpNotChecked') }}</span></div>
                  <ol class="segments"><li v-for="(segment, index) in revisionForCost(candidate.nodeIds).segments" :key="index"><strong>{{ index ? `${nodeName(candidate.nodeIds[index - 1]!)} → ${nodeName(candidate.nodeIds[index]!)}` : tr('clientEntry', { name: nodeName(candidate.nodeIds[index]!) }) }}</strong><span>{{ segment?.value == null ? tr('notMeasured') : tr('milliseconds', { value: format.number(segment.value) }) }} <NTag v-if="segment && Date.parse(segment.expiresAt) <= Date.now()" size="small" type="warning">{{ tr('stale') }}</NTag></span><small>{{ segment ? `${segment.method} · ${segment.transport} · ${tr('samples', { count: segment.samples })} · ${tr('dispersion', { value: segment.dispersion == null ? tr('unknown') : format.number(segment.dispersion) })}` : tr('missingMethod') }}</small><small v-if="segment">{{ tr('observedAt', { time: format.date(segment.observedAt) }) }} · {{ tr('expiresAt', { time: format.date(segment.expiresAt) }) }} · {{ segment.timingBasis ?? tr('unknown') }}</small></li></ol>
                  <p v-if="group.key === 'partial' && revisionForCost(candidate.nodeIds).nodeCost !== null">{{ tr('nodeCost', { value: format.number(revisionForCost(candidate.nodeIds).nodeCost) }) }}</p>
                  <details><summary>{{ tr('protocolCombinations') }}</summary><p>{{ tr('protocolHint') }}</p><div v-for="(nodeId, index) in candidate.nodeIds" :key="nodeId">{{ nodeName(nodeId) }}: {{ isReverse && index ? 'SS 2022 TCP' : protocols(workspace.nodes.find(n => n.id === nodeId), form.allowUdpTransport).join(' / ') }}</div><NFormItem :label="tr('entryProtocol')"><NSelect v-model:value="protocolChoices[candidate.id]" :placeholder="sourceProtocol(workspace.nodes.find(n => n.id === candidate.nodeIds[0]), 0)" :options="protocols(workspace.nodes.find(n => n.id === candidate.nodeIds[0]), form.allowUdpTransport).map(value => ({ label: value, value }))" /></NFormItem><NButton :disabled="!protocolChoices[candidate.id]" @click="selectCandidate(candidate, protocolChoices[candidate.id])">{{ tr('useCombination') }}</NButton></details>
                </article>
              </section>
            </template>
            <details><summary>{{ tr('manualPath') }}</summary><NAlert v-if="isReverse" type="info">{{ tr('reverseLocked') }}</NAlert><template v-else><p>{{ tr('manualHint') }}</p><NFormItem :label="tr('entryAndRelays')"><NSelect v-model:value="manualNodes" multiple :max="5" :options="manualOptions" filterable /></NFormItem><ol><li v-for="(nodeId, index) in manualNodes" :key="nodeId">{{ nodeName(nodeId) }} <NButton size="small" :disabled="!index" @click="[manualNodes[index - 1], manualNodes[index]] = [manualNodes[index]!, manualNodes[index - 1]!]">{{ tr('moveUp') }}</NButton><NButton size="small" :disabled="index === manualNodes.length - 1" @click="[manualNodes[index], manualNodes[index + 1]] = [manualNodes[index + 1]!, manualNodes[index]!]">{{ tr('moveDown') }}</NButton></li><li>{{ exit?.name ?? tr('chooseExit') }} · {{ tr('terminalLocked') }}</li></ol><NButton :disabled="!exit" @click="addManual">{{ tr('addManual') }}</NButton></template></details>
          </section>
          <section v-if="step === 2" class="panel">
            <h2>{{ tr('steps.options') }}</h2>
            <div v-if="!paths.length" class="empty"><p>{{ tr('errors.selectPath') }}</p><NButton @click="go(1)">{{ tr('steps.paths') }}</NButton></div>
            <template v-else>
              <div class="path-tabs" role="group" :aria-label="tr('pathDrafts')"><NButton v-for="path in paths" :key="path.id" :type="activePath?.id === path.id ? 'primary' : 'default'" @click="form.currentPathId = path.id">{{ path.name || tr('unnamed') }} · {{ tr('issueCount', { count: pathErrors(path).length }) }}</NButton></div>
              <template v-if="activePath">
                <NFormItem :label="tr('routeName')" required><NInput v-model:value="activePath.name" :maxlength="80" :input-props="{ 'aria-label': tr('routeName') }" show-count /></NFormItem>
                <RoutePath :nodes="pathNodes(activePath.revision.nodeIds)" />
                <ul v-if="pathErrors(activePath).length" class="field-issues"><li v-for="issue in pathErrors(activePath)" :key="issue">{{ tr(`errors.${issue}`) }}</li></ul>
                <div class="egress-row"><NFormItem :label="tr('egressPolicy')"><NSelect :value="activePath.revision.policyId ?? ''" :options="policyOptions" @update:value="setPolicy" /></NFormItem><NButton size="small" @click="drawer = true">{{ tr('egressRouting') }}</NButton></div>
                <p class="muted">{{ tr('policyIsolation') }}</p>
                <details><summary>{{ tr('advanced') }}</summary>
                  <NAlert v-if="isReverse" type="warning">{{ tr('reverseLocked') }}</NAlert>
                  <div class="actions"><NButton :disabled="isReverse" @click="go(1)">{{ tr('changeFixedPath') }}</NButton></div>
                  <article v-for="(nodeId, index) in activePath.revision.nodeIds" :key="`${activePath.id}-${index}`" class="listener-editor">
                    <h3>{{ index === 0 ? tr('entryListener') : tr('hopListener', { index }) }} · {{ nodeName(nodeId) }}</h3>
                    <NFormItem :label="tr('actualListener')"><NSelect :value="listenerIds(activePath.revision)[index]" :options="listenerOptions(activePath, index)" :disabled="isReverse && index > 0" @update:value="value => changeListener(activePath!, index, value)" /></NFormItem>
                    <div class="listener-grid"><NFormItem :label="tr('protocol')"><NSelect :value="selectedListener(activePath, index)?.protocol" :options="protocols(workspace.nodes.find(n => n.id === nodeId), form.allowUdpTransport).map(value => ({ label: value, value }))" :disabled="isReverse && index > 0" @update:value="value => allocateListener(activePath!, index, value)" /></NFormItem><NFormItem :label="tr('newPrivatePort')"><NInputNumber v-model:value="portInputs[`${activePath.id}-${index}`]" :min="1" :max="65535" :precision="0" :placeholder="tr('autoPort')" :disabled="isReverse && index > 0" /></NFormItem></div>
                    <NButton :disabled="isReverse && index > 0" @click="allocateListener(activePath!, index)">{{ tr('allocateListener') }}</NButton>
                    <p class="muted">{{ selectedListener(activePath, index)?.address }}:{{ selectedListener(activePath, index)?.port }} · {{ selectedListener(activePath, index)?.transport }} · {{ tr('actualPreview') }}</p>
                  </article>
                </details>
                <div class="table-scroll"><table><caption>{{ tr('portsPreview') }}</caption><thead><tr><th>{{ tr('node') }}</th><th>{{ tr('protocol') }}</th><th>{{ tr('listenAddress') }}</th><th>{{ tr('actualPort') }}</th><th>{{ tr('transport') }}</th></tr></thead><tbody><tr v-for="(nodeId, index) in activePath.revision.nodeIds" :key="nodeId"><td>{{ nodeName(nodeId) }}</td><td>{{ selectedListener(activePath, index)?.protocol ?? tr('missing') }}</td><td>{{ selectedListener(activePath, index)?.address ?? tr('missing') }}</td><td>{{ selectedListener(activePath, index)?.port ?? tr('missing') }}</td><td>{{ selectedListener(activePath, index)?.transport ?? tr('missing') }}</td></tr></tbody></table></div>
                <NButton v-if="!props.routeId" type="error" secondary @click="form.selectedPaths = paths.filter(p => p.id !== activePath!.id)">{{ tr('removePath') }}</NButton>
              </template>
            </template>
          </section>
          <section v-if="step === 3" class="panel review">
            <h2>{{ tr('steps.review') }}</h2>
            <NAlert type="info">{{ tr('reviewNotice') }}</NAlert>
            <p v-if="source?.published">{{ tr('publishedPreserved', { revision: source.published.revision }) }}</p>
            <p>{{ tr('affectedNodes') }}: <template v-for="(nodeId, index) in affectedNodes" :key="nodeId"><span v-if="index">, </span><RouterLink :to="entityLocation('node', nodeId)">{{ nodeName(nodeId) }}</RouterLink></template></p>
            <p>{{ tr('affectedSubscriptions') }}: <template v-for="(subscription, index) in affectedSubscriptions" :key="subscription.id"><span v-if="index">, </span><RouterLink :to="entityLocation('subscription', subscription.id)">{{ subscription.name }}</RouterLink></template><span v-if="!affectedSubscriptions.length">{{ tr('none') }}</span></p>
            <article v-for="path in paths" :key="path.id" class="review-path">
              <h3>{{ path.name || tr('unnamed') }}</h3><RoutePath :nodes="pathNodes(path.revision.nodeIds)" />
              <p>{{ tr('revisionChange', { from: source?.published?.revision ?? 0, to: path.revision.revision }) }} · {{ tr('policyLabel', { name: workspace.policies.find(p => p.id === path.revision.policyId)?.name ?? tr('nodeDefault'), revision: path.revision.policyRevision ?? 0 }) }}</p>
              <dl class="diff" v-if="source?.published"><dt>{{ tr('previousPath') }}</dt><dd>{{ source.published.nodeIds.map(nodeName).join(' → ') }}</dd><dt>{{ tr('nextPath') }}</dt><dd>{{ path.revision.nodeIds.map(nodeName).join(' → ') }}</dd><dt>{{ tr('previousListeners') }}</dt><dd>{{ listenerIds(source.published).join(', ') }}</dd><dt>{{ tr('nextListeners') }}</dt><dd>{{ listenerIds(path.revision).join(', ') }}</dd></dl>
              <p>{{ form.requireUdpRelay ? tr('udpRequired') : tr('businessUdpRejected') }} · {{ tr('authenticationNotChecked') }}</p>
              <ul class="port-list"><li v-for="(nodeId, index) in path.revision.nodeIds" :key="nodeId">{{ nodeName(nodeId) }} · {{ selectedListener(path, index)?.protocol }} · {{ selectedListener(path, index)?.address }}:{{ selectedListener(path, index)?.port }} / {{ selectedListener(path, index)?.transport }} · {{ tr('listenerNotChecked') }}</li><template v-for="link in path.revision.links" :key="link.listenerId"><li v-if="link.tunnelId">{{ tr('mappingPreview', { port: workspace.tunnels.find(t => t.id === link.tunnelId)?.mappingPort ?? 0, endpoint: workspace.tunnels.find(t => t.id === link.tunnelId)?.relayEndpoint ?? tr('missing') }) }}</li></template></ul>
              <NTag :type="path.revision.validated ? 'success' : 'warning'">{{ path.revision.validated ? tr('simulatedValidated') : tr('needsValidation') }}</NTag>
              <ul v-if="pathErrors(path).length" class="field-issues"><li v-for="issue in pathErrors(path)" :key="issue">{{ tr(`errors.${issue}`) }}</li></ul>
            </article>
            <p>{{ tr('firewallNotice') }}</p><p>{{ tr('externalEgressNotice') }}</p>
            <div class="actions"><NButton :disabled="busy || !paths.length" @click="validate">{{ tr('validate') }}</NButton></div>
            <NFormItem :label="tr('simulationOutcome')"><NSelect v-model:value="outcome" :disabled="busy" :options="(['scenario', 'success', 'failure', 'rollback_pending', 'rollback_failed', 'partial'] as const).map(value => ({ label: tr(`outcomes.${value}`), value }))" /></NFormItem>
          </section>
        </fieldset>
        <NAlert v-if="busy" type="info" role="status" aria-live="polite">{{ phase === 'applied' ? tr('applyingNotice') : tr('validatingNotice') }} <NButton v-if="phase === 'validation'" @click="publishTask.cancel">{{ tr('cancelBeforeApply') }}</NButton></NAlert>
        <section v-if="results.length" class="panel" aria-live="polite"><h2>{{ tr('batchResults') }}</h2><p>{{ tr('batchNotice', { completed: results.length, total: paths.length }) }}</p><article v-for="result in results" :key="result.releaseId" class="batch-result"><strong>{{ result.name }}</strong><NTag :type="result.state === 'published' ? 'success' : 'warning'">{{ t(`status.${result.state}`) }}</NTag><RouterLink :to="entityLocation('release', result.releaseId)">{{ tr('viewRelease') }}</RouterLink><RouterLink :to="entityLocation('route', result.routeId)">{{ tr('viewRoute') }}</RouterLink><RouterLink v-if="result.state === 'published'" :to="pageLocation('subscription-new', {}, { routeIds: result.routeId })">{{ tr('createSubscription') }}</RouterLink></article><RouterLink :to="pageLocation('activity', {}, { tab: 'releases' })">{{ tr('allReleases') }}</RouterLink></section>
      </div>
      <aside class="summary panel" :aria-label="tr('draftSummary')">
        <span class="eyebrow">{{ tr('draftSummary') }}</span><h2>{{ exit?.name ?? tr('chooseExit') }}</h2><dl><dt>{{ tr('environment') }}</dt><dd>{{ tr(form.environment === 'mainland_china' ? 'mainland' : 'outside') }}</dd><dt>{{ tr('transportUdp') }}</dt><dd>{{ form.allowUdpTransport ? tr('allowed') : tr('disabled') }}</dd><dt>{{ tr('businessUdp') }}</dt><dd>{{ form.requireUdpRelay ? tr('requiredVerified') : tr('rejected') }}</dd><dt>{{ tr('selectedPaths') }}</dt><dd>{{ paths.length }}</dd></dl>
        <div v-for="path in paths" :key="path.id" class="summary-path"><strong>{{ path.name || tr('unnamed') }}</strong><p>{{ path.revision.nodeIds.map(nodeName).join(' → ') }}</p><small>{{ workspace.policies.find(p => p.id === path.revision.policyId)?.name ?? tr('nodeDefault') }} · {{ path.revision.validated ? tr('simulatedValidated') : tr('needsValidation') }}</small></div>
        <NAlert v-if="allIssues.length" type="warning" :title="tr('pendingIssues', { count: allIssues.length })"><ul><li v-for="issue in [...new Set(allIssues)].slice(0, 5)" :key="issue">{{ tr(`errors.${issue}`) }}</li></ul></NAlert>
        <p v-else class="muted">{{ tr('noBlockingIssues') }}</p><small>{{ draft.dirty.value ? tr('unsavedChanges') : tr('savedBaseline') }}</small>
      </aside>
    </div>
    <footer class="editor-footer"><div class="actions"><NButton :disabled="busy || !step" @click="go(step - 1)">{{ tr('back') }}</NButton><NButton :disabled="busy" @click="saveDraft">{{ tr('saveDraft') }}</NButton><NButton :disabled="busy || !draft.dirty.value" @click="discard">{{ tr('discard') }}</NButton></div><NButton v-if="step < 3" type="primary" :disabled="busy" @click="go(step + 1)">{{ tr('next') }}</NButton><NButton v-else type="primary" :loading="busy" :disabled="busy || !!allIssues.length || !paths.every(p => p.revision.validated)" @click="publish">{{ tr('simulatePublish', { count: paths.length }) }}</NButton></footer>
    <EgressPolicyDrawer v-if="activePath && exit" v-model:show="drawer" :terminal-node-id="exit.id" :route-id="props.routeId" :draft-id="`${id}-${activePath.id}`" :policy-id="activePath.revision.policyId" :readonly="busy" @save="savePolicy" />
  </div>
</template>

<style scoped>
.route-editor { padding-bottom: 100px; }
.muted, small { color: var(--color-muted); }.wizard-steps { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; margin: 24px 0; }.wizard-steps button { display: flex; align-items: center; gap: 10px; min-height: 58px; text-align: left; border: 1px solid var(--color-border); border-radius: 10px; padding: 10px 12px; background: white; color: var(--color-muted); cursor: pointer; }.wizard-steps button span { border-radius: 50%; width: 26px; height: 26px; flex-shrink: 0; display: grid; place-items: center; background: #f0f4f0; }.wizard-steps button.current { border-color: var(--color-primary); color: var(--color-primary); background: #f0f7f2; font-weight: 600; }.editor-layout { display: grid; grid-template-columns: minmax(0, 1fr) 284px; gap: 24px; align-items: start; }.editor-main { min-width: 0; }.editor-main > .n-alert { margin-bottom: 16px; }.editor-fields { border: 0; padding: 0; margin: 0; min-width: 0; }.editor-fields:disabled { opacity: .7; pointer-events: none; }.summary { position: sticky; top: 20px; padding: 24px; }.summary h2 { margin-top: 12px; }.summary dt { font-size: 12px; margin-top: 16px; }.summary dd { font-family: inherit; }.summary-path { margin: 20px 0; padding-top: 12px; border-top: 1px solid var(--color-border); }.summary-path p { margin: 8px 0; }.summary ul, .field-issues { padding-left: 20px; }.summary li { margin: 8px 0; }.exit-summary, .candidate-top, .cost-row { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 10px; }.exit-summary { padding: 18px; background: #f5f8f4; border-radius: 8px; margin: 0 0 24px; }.exit-summary p { margin: 8px 0 0; }.preference { margin: 16px 0; }.preference p { margin: 4px 0 12px 25px; font-size: 12px; }.recommend-controls { display: flex; align-items: center; gap: 16px; }.recommend-controls .n-form-item { width: 160px; }.measure-controls { margin: 24px 0 16px; }.measurement-group { margin: 24px 0; }.measurement-group h3 { display: flex; gap: 12px; align-items: center; }.candidate { border: 1px solid var(--color-border); padding: 20px; border-radius: 10px; margin: 16px 0; }.candidate.selected { border-color: var(--color-primary); background: #f6faf7; }.cost-row { font-size: 13px; margin: 16px 0; }.cost-row span { color: var(--color-muted); }.segments { padding-left: 20px; }.segments li { display: grid; gap: 5px; padding-bottom: 12px; }.segments li span { display: flex; align-items: center; gap: 8px; }.segments small { overflow-wrap: anywhere; }.path-tabs { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 24px; }.path-tabs .n-button { max-width: 100%; height: auto; min-height: 44px; white-space: normal; }.egress-row { display: flex; gap: 16px; align-items: center; margin-top: 24px; }.egress-row .n-form-item { flex: 1; min-width: 0; }.listener-editor { border-top: 1px solid var(--color-border); margin-top: 16px; padding-top: 16px; }.listener-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }table { width: 100%; border-collapse: collapse; white-space: nowrap; text-align: left; margin: 16px 0; }caption { text-align: left; font-weight: 600; margin-bottom: 12px; }th, td { padding: 12px; border-bottom: 1px solid var(--color-border); }th { font-size: 12px; color: var(--color-muted); }.field-issues { color: #ae3636; }.review-path { padding: 20px 0; border-bottom: 1px solid var(--color-border); }.port-list { padding-left: 20px; }.port-list li { padding: 8px 0; }.diff { display: grid; grid-template-columns: 130px minmax(0, 1fr); gap: 8px; font-size: 12px; }.diff dd { font-family: inherit; }.batch-result { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; padding: 16px 0; border-bottom: 1px solid var(--color-border); }.empty { text-align: center; padding: 28px 12px; }.editor-footer { position: sticky; bottom: 0; z-index: 5; border: 1px solid var(--color-border); background: #fffffff5; box-shadow: 0 -4px 20px #24342f08; padding: 16px; display: flex; justify-content: space-between; flex-wrap: wrap; align-items: center; gap: 12px; border-radius: 12px; }.editor-footer .actions { margin: 0; }.editor-footer .n-button { min-height: 44px; }
@media (max-width: 1100px) { .editor-layout { grid-template-columns: minmax(0, 1fr); }.summary { position: static; order: -1; }.summary dl { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }.summary dd { align-self: end; }.summary-path { display: inline-block; margin: 8px 20px 12px 0; }.summary > .n-alert { margin-top: 12px; } }
@media (max-width: 600px) { .wizard-steps { grid-template-columns: repeat(2, minmax(0, 1fr)); }.candidate { padding: 16px 12px; }.listener-grid { grid-template-columns: 1fr; }.egress-row { display: block; }.recommend-controls { flex-wrap: wrap; }.editor-footer { padding: 12px; }.editor-footer .n-button { flex: 1; }.editor-footer .actions { width: 100%; }.diff { grid-template-columns: 1fr; }.summary { padding: 18px; } }
</style>
