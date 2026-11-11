<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NInput, NPagination, NSelect, NTabPane, NTabs } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import { pageLocation, entityLocation } from '../../app/router'
import { resetEpoch, useWorkspace } from '../../demo/workspace'
import { useFormat } from '../../composables/useFormat'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import { releaseNodeIds, releaseSources, sourceName, tailText, type Source } from './activity-model'
import ActivitySourceLink from './ActivitySourceLink.vue'

const { t, te } = useI18n()
const f = (key: string, params: Record<string, string | number> = {}) => t(`features.activity.${key}`, params)
const label = (group: string, value: string) => te(`features.activity.${group}.${value}`) ? f(`${group}.${value}`) : value
const route = useRoute()
const router = useRouter()
const { workspace } = useWorkspace()
const { date } = useFormat()
const query = (key: string) => typeof route.query[key] === 'string' ? route.query[key] : ''
const tab = computed(() => query('tab') || 'events')
function updateQuery(key: string, value: string | number | null) {
  const next: Record<string, unknown> = { ...route.query, [key]: value == null || value === '' ? undefined : String(value) }
  if (key !== 'page') delete next.page
  void router.replace(pageLocation('activity', {}, Object.fromEntries(Object.entries(next).filter((entry): entry is [string, string] => typeof entry[1] === 'string'))))
}
const timeRange = ref('all')
const activityReadFailed = ref(workspace.scenario === 'error')
watch(() => workspace.scenario, value => { activityReadFailed.value = value === 'error' })
const allOption = computed(() => ({ label: f('filters.all'), value: '' }))
const entityOptions = computed(() => [allOption.value, ...['node', 'route', 'subscription', 'release'].map(value => ({ label: f(`types.${value}`), value }))])
const timeOptions = computed(() => [{ label: f('filters.allTime'), value: 'all' }, { label: f('filters.day'), value: 'day' }, { label: f('filters.week'), value: 'week' }])
type ActivityRow = { id: string; name: string; kind: string; status: string; updatedAt: string; sources: Source[]; nodeIds: string[]; releaseId?: string; revision?: number; cleanup?: string; pendingSync?: boolean; unverified?: boolean }
const rows = computed<ActivityRow[]>(() => tab.value === 'releases' ? workspace.releases.map(release => ({
  id: release.id, name: release.name, kind: release.kind,
  status: release.issue?.code === 'features.activity.issues.CANCELLED' ? 'cancelled' : release.state,
  updatedAt: release.updatedAt, sources: releaseSources(release), nodeIds: releaseNodeIds(release, workspace), releaseId: release.id, revision: release.revision, cleanup: release.cleanup,
  pendingSync: releaseNodeIds(release, workspace).some(id => workspace.nodes.find(node => node.id === id)?.control !== 'online'),
  unverified: release.state === 'published' && (!release.nodes.length || release.nodes.some(node => !node.applied || !node.verified) || release.stages?.some(stage => stage.state !== 'succeeded')),
})) : workspace.events.map(event => ({ id: event.id, name: event.name, kind: event.kind, status: event.status, updatedAt: event.updatedAt, sources: [{ kind: event.entityType, id: event.entityId }], nodeIds: [], releaseId: event.releaseId })))
const kindOptions = computed(() => [allOption.value, ...[...new Set(rows.value.map(row => row.kind))].sort().map(value => ({ value, label: label('kinds', value) }))])
const statusOptions = computed(() => [allOption.value, ...[...new Set([...rows.value.map(row => row.status), ...(tab.value === 'releases' ? ['cleanup_pending', 'node_sync_pending', 'applied_unverified'] : [])])].sort().map(value => ({ value, label: label('states', value) }))])
const invalidFilter = computed(() => !!query('entityType') && !entityOptions.value.some(option => option.value === query('entityType')) || !!query('kind') && !kindOptions.value.some(option => option.value === query('kind')) || !!query('status') && !statusOptions.value.some(option => option.value === query('status')))
const filtered = computed(() => {
  const text = query('q').trim().toLowerCase()
  const cutoff = timeRange.value === 'all' ? 0 : Date.now() - (timeRange.value === 'day' ? 1 : 7) * 86400000
  const result = rows.value.filter(row => {
    const objectRefs = [...row.sources, ...(row.releaseId ? [{ kind: 'release' as const, id: row.releaseId }] : []), ...row.nodeIds.map(id => ({ kind: 'node' as const, id }))]
    return (!query('kind') || row.kind === query('kind')) &&
      (!query('status') || row.status === query('status') || query('status') === 'cleanup_pending' && row.cleanup === 'pending' || query('status') === 'node_sync_pending' && row.pendingSync || query('status') === 'applied_unverified' && row.unverified) &&
      (!query('entityType') && !query('entityId') || objectRefs.some(source => (!query('entityType') || source.kind === query('entityType')) && (!query('entityId') || source.id === query('entityId')))) &&
      (!cutoff || Date.parse(row.updatedAt) >= cutoff) &&
      (!text || [row.id, row.name, row.kind, ...row.sources.flatMap(source => [source.id, sourceName(workspace, source) ?? ''])].join(' ').toLowerCase().includes(text))
  })
  const sort = query('sort') || '-updatedAt'
  return result.sort((a, b) => (sort.includes('name') ? a.name.localeCompare(b.name) : a.updatedAt.localeCompare(b.updatedAt)) * (sort.startsWith('-') ? -1 : 1) || a.id.localeCompare(b.id))
})
const pageSize = computed(() => Math.min(50, Number(query('pageSize')) || 10))
const page = computed(() => Math.min(Math.max(1, Number(query('page')) || 1), Math.max(1, Math.ceil(filtered.value.length / pageSize.value))))
const visibleRows = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
function clearFilters() { timeRange.value = 'all'; void router.replace(pageLocation('activity', {}, { tab: tab.value })) }

const logTask = useSimulatedTask()
const selectedNode = computed(() => query('entityType') === 'node' ? query('entityId') : '')
const component = ref('agent')
const lineLimit = ref(50)
const byteLimit = ref(4096)
const logError = ref('')
const logResult = ref<{ text: string; bytes: number; lines: number; truncated: boolean; node: string; component: string; time: string; stale: boolean }>()
const logNode = computed(() => workspace.nodes.find(node => node.id === selectedNode.value))
const componentOptions = computed(() => [
  { label: 'Agent', value: 'agent' }, { label: 'Mihomo', value: 'mihomo' },
  ...(logNode.value?.id === workspace.settings.relayNodeId && logNode.value.role === 'both' ? [{ label: 'frps', value: 'frps' }] : []),
  ...(logNode.value?.mode === 'reverse' ? [{ label: 'frpc', value: 'frpc' }] : []),
])
async function selectLogNode(value: string | null) {
  await router.replace(pageLocation('activity', {}, { ...route.query, tab: 'logs', entityType: 'node', entityId: value ?? undefined, page: undefined }))
}
watch([selectedNode, component, lineLimit, byteLimit, tab], () => { logTask.cancel(); logResult.value = undefined; logError.value = '' })
watch(resetEpoch, () => { logTask.cancel(); logResult.value = undefined; logError.value = '' })
async function loadLogs() {
  if (logTask.state.value === 'running') return
  logError.value = ''
  const node = logNode.value
  if (!node) { logError.value = f('logs.required'); return }
  if (!componentOptions.value.some(option => option.value === component.value)) { logError.value = f('logs.componentUnavailable'); return }
  if (node.control !== 'online' || node.removedAt) { logError.value = f('logs.unavailable'); return }
  const epoch = resetEpoch.value
  const succeeded = await logTask.run(workspace.scenario === 'error' ? 'error' : 'success', 450)
  if (!succeeded) { if (logTask.state.value === 'error') logError.value = f('logs.failed'); return }
  if (epoch !== resetEpoch.value || logNode.value?.control !== 'online') { logError.value = f('logs.unavailable'); return }
  const records: { time: string; text: string }[] = []
  const add = (time: string, text: string) => records.push({ time, text })
  if (component.value === 'agent') {
    add(node.updatedAt, f('logs.agentSummary', { time: node.updatedAt, control: node.control, verification: node.verification }))
    for (const event of workspace.events.filter(event => event.entityType === 'node' && event.entityId === node.id || event.releaseId && workspace.releases.find(release => release.id === event.releaseId)?.nodes.some(record => record.nodeId === node.id))) add(event.updatedAt, f('logs.eventSummary', { time: event.updatedAt, kind: event.kind, name: event.name, status: event.status }))
  } else if (component.value === 'mihomo') add(node.updatedAt, f('logs.mihomoSummary', { time: node.updatedAt, count: workspace.listeners.filter(listener => listener.nodeId === node.id).length }))
  else if (component.value === 'frps') add(node.updatedAt, f('logs.frpsSummary', { time: node.updatedAt, count: workspace.tunnels.filter(tunnel => tunnel.relayNodeId === node.id).length }))
  else add(node.updatedAt, f('logs.frpcSummary', { time: node.updatedAt, state: node.tunnel }))
  for (const release of workspace.releases) {
    const result = release.nodes.find(record => record.nodeId === node.id)
    if (result) add(release.updatedAt, f('logs.releaseSummary', { time: release.updatedAt, id: release.id, applied: String(result.applied), verified: String(result.verified), rollback: result.rollback }))
    for (const stage of release.stages ?? []) if (stage.nodeId === node.id) add(stage.completedAt ?? release.updatedAt, f('logs.stageSummary', { time: stage.completedAt ?? release.updatedAt, name: stage.name, state: stage.state }))
  }
  const tail = tailText(records.sort((a, b) => a.time.localeCompare(b.time)).map(record => record.text), lineLimit.value, byteLimit.value)
  logResult.value = { ...tail, node: node.name, component: component.value, time: new Date().toISOString(), stale: workspace.scenario === 'stale' }
}
</script>

<template>
  <PageHeader :title="f('ActivityPage.title')" :description="f('ActivityPage.goal')" />
  <NTabs :value="tab" type="line" size="large" @update:value="value => updateQuery('tab', value)">
    <NTabPane v-for="name in ['events', 'releases', 'logs']" :key="name" :name="name" :tab="f(`tabs.${name}`)" />
  </NTabs>
  <template v-if="tab !== 'logs'">
    <section class="panel filters" :aria-label="f('filters.search')">
      <div><label for="activity-search">{{ f('filters.search') }}</label><NInput id="activity-search" :value="query('q')" :maxlength="120" clearable @update:value="value => updateQuery('q', value)" /></div>
      <div><label for="activity-kind">{{ f('filters.kind') }}</label><NSelect id="activity-kind" :value="query('kind')" :options="kindOptions" @update:value="value => updateQuery('kind', value)" /></div>
      <div><label for="activity-type">{{ f('filters.entityType') }}</label><NSelect id="activity-type" :value="query('entityType')" :options="entityOptions" @update:value="value => updateQuery('entityType', value)" /></div>
      <div><label for="activity-object">{{ f('filters.entityId') }}</label><NInput id="activity-object" :value="query('entityId')" :maxlength="160" clearable @update:value="value => updateQuery('entityId', value)" /></div>
      <div><label for="activity-status">{{ f('filters.status') }}</label><NSelect id="activity-status" :value="query('status')" :options="statusOptions" @update:value="value => updateQuery('status', value)" /></div>
      <div><label for="activity-time">{{ f('filters.time') }}</label><NSelect id="activity-time" v-model:value="timeRange" :options="timeOptions" /></div>
      <div class="filter-foot"><p>{{ f('filters.scopeHint') }}</p><NButton @click="clearFilters">{{ f('filters.clear') }}</NButton></div>
    </section>
    <NAlert v-if="invalidFilter" type="warning" class="notice">{{ f('filters.unknown') }}</NAlert>
    <NAlert v-if="activityReadFailed" type="error" class="notice"><p>{{ f('queryFailure') }}</p><NButton @click="activityReadFailed = workspace.scenario === 'error'">{{ f('refresh') }}</NButton></NAlert>
    <section class="panel activity-list">
      <div class="list-toolbar"><p role="status">{{ f('count', { count: filtered.length }) }}</p><label class="sort-control">{{ f('filters.sort') }}<NSelect :value="query('sort') || '-updatedAt'" :options="[{ label: f('filters.newest'), value: '-updatedAt' }, { label: f('filters.oldest'), value: 'updatedAt' }, { label: f('filters.nameAsc'), value: 'name' }, { label: f('filters.nameDesc'), value: '-name' }]" @update:value="value => updateQuery('sort', value)" /></label></div>
      <div v-if="visibleRows.length" class="table-scroll"><table><thead><tr><th>{{ f(tab === 'releases' ? 'columns.task' : 'columns.event') }}</th><th>{{ f('columns.action') }}</th><th>{{ f('columns.source') }}</th><th>{{ f('columns.result') }}</th><th>{{ f('columns.updated') }}</th></tr></thead><tbody>
        <tr v-for="row in visibleRows" :key="row.id"><td><RouterLink v-if="tab === 'releases'" :to="entityLocation('release', row.id)">{{ row.name }}</RouterLink><strong v-else>{{ row.name }}</strong><small>{{ row.id }}</small><small v-if="row.revision !== undefined">{{ f('columns.revision') }} {{ row.revision }}</small></td>
          <td>{{ label('kinds', row.kind) }}</td><td><div v-for="source in row.sources" :key="`${source.kind}-${source.id}`" class="source"><span>{{ f(`types.${source.kind}`) }} · </span><ActivitySourceLink :source="source" /></div><RouterLink v-if="tab === 'events' && row.releaseId" :to="entityLocation('release', row.releaseId)">{{ f('details') }} · {{ row.releaseId }}</RouterLink></td>
          <td><span class="result" :class="{ warning: row.status.includes('failed') || row.status.includes('pending') || row.unverified }">{{ label('states', row.unverified ? 'applied_unverified' : row.status) }}</span><small v-if="row.pendingSync">{{ f('states.node_sync_pending') }}</small><small v-if="row.cleanup === 'pending'">{{ f('states.cleanup_pending') }}</small></td><td><time :datetime="row.updatedAt">{{ date(row.updatedAt) }}</time></td></tr>
      </tbody></table></div>
      <EmptyState v-else :title="f('empty')" :description="f('emptyHint')" filtered @clear="clearFilters" />
      <div class="pagination"><label>{{ f('filters.pageSize') }}<NSelect :value="pageSize" :options="[10, 25, 50].map(value => ({ label: String(value), value }))" @update:value="value => updateQuery('pageSize', value)" /></label><NPagination :page="page" :page-size="pageSize" :item-count="filtered.length" :page-slot="5" @update:page="value => updateQuery('page', value)" /></div>
      <p class="muted">{{ f('snapshot') }}</p>
    </section>
  </template>
  <template v-else>
    <section class="panel"><h2>{{ f('logs.title') }}</h2><p>{{ f('logs.hint') }}</p>
      <div class="log-fields">
        <div><label for="log-node">{{ f('logs.node') }}</label><NSelect id="log-node" :value="selectedNode || null" :options="workspace.nodes.map(node => ({ label: `${node.name} · ${label('states', node.control)}`, value: node.id }))" filterable clearable @update:value="selectLogNode" /></div>
        <div><label for="log-component">{{ f('logs.component') }}</label><NSelect id="log-component" v-model:value="component" :options="componentOptions" /></div>
        <div><label for="log-lines">{{ f('logs.lines') }}</label><NSelect id="log-lines" v-model:value="lineLimit" :options="[20, 50, 100, 200].map(value => ({ label: String(value), value }))" /></div>
        <div><label for="log-bytes">{{ f('logs.bytes') }}</label><NSelect id="log-bytes" v-model:value="byteLimit" :options="[1024, 4096, 16384, 65536].map(value => ({ label: `${value / 1024} KiB`, value }))" /></div>
      </div>
      <div class="actions"><NButton type="primary" :loading="logTask.state.value === 'running'" @click="loadLogs">{{ f('logs.load') }}</NButton><NButton @click="logTask.cancel(); logResult = undefined; logError = ''">{{ f('logs.clear') }}</NButton><ActivitySourceLink v-if="selectedNode" :source="{ kind: 'node', id: selectedNode }" /></div>
    </section>
    <NAlert v-if="logError" type="error" class="notice" role="alert">{{ logError }}<p v-if="logResult">{{ f('logs.previous') }}</p></NAlert>
    <section class="panel" aria-live="polite">
      <template v-if="logResult"><h2>{{ f('logs.scope', { node: logResult.node, component: logResult.component, lines: lineLimit, bytes: byteLimit }) }}</h2><p>{{ f('logs.result', { lines: logResult.lines, bytes: logResult.bytes }) }} · {{ f('logs.loaded', { time: date(logResult.time) }) }}</p><NAlert v-if="logResult.stale" type="warning">{{ f('logs.stale') }}</NAlert><p v-if="logResult.truncated">{{ f('logs.truncated') }}</p><pre v-if="logResult.text" tabindex="0" :aria-label="f('logs.output')">{{ logResult.text }}</pre><p v-else>{{ f('logs.empty') }}</p></template>
      <p v-else>{{ f('logs.notLoaded') }}</p>
    </section>
  </template>
</template>

<style scoped>
.filters, .log-fields { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 20px; }
.filter-foot { grid-column: 1 / -1; display: flex; flex-wrap: wrap; gap: 16px; align-items: center; justify-content: space-between; }
.filter-foot p { margin: 0; max-width: 720px; font-size: 12px; }
.notice { margin-bottom: 20px; }.activity-list { margin-top: 24px; }
.list-toolbar, .pagination { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 20px; align-items: center; margin-bottom: 20px; }
.sort-control { width: 220px; }.pagination { margin: 24px 0 0; }.pagination label { width: 130px; }
table { width: 100%; border-collapse: collapse; min-width: 740px; text-align: left; }th { color: var(--color-muted); font-size: 12px; font-weight: 500; background: #f7faf8; }th, td { padding: 16px 12px; border-bottom: 1px solid var(--color-border); vertical-align: top; }td:first-child { max-width: 300px; }small { display: block; color: var(--color-muted); margin-top: 6px; }.source { margin-bottom: 8px; }.source span { color: var(--color-muted); font-size: 12px; }.result { display: inline-block; border-radius: 6px; padding: 4px 8px; color: #1d6655; background: #eaf3ed; }.result.warning { color: #8a511a; background: #fff3e6; }.muted { font-size: 12px; }pre { max-height: 440px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; background: #f4f7f4; padding: 20px; border-radius: 8px; font-size: 12px; line-height: 1.8; }.actions :deep(.n-button), .filter-foot :deep(.n-button) { min-height: 44px; }.log-fields { grid-template-columns: repeat(4, minmax(0, 1fr)); }
@media (max-width: 1000px) { .filters, .log-fields { grid-template-columns: repeat(2, minmax(0, 1fr)); } }@media (max-width: 560px) { .filters, .log-fields { grid-template-columns: 1fr; }.pagination { align-items: flex-start; }.sort-control { width: 100%; } }
</style>
