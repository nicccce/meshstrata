<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NInput, NPagination } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import QueryStamp from '../../components/QueryStamp.vue'
import { entityLocation, pageLocation } from '../../app/router'
import { resetEpoch, useWorkspace } from '../../demo/workspace'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import { useFormat } from '../../composables/useFormat'
import type { Node } from '../../domain'
import { automaticLocation, environments, locationKey, statusMatches } from './node-model'

const { t } = useI18n()
const tr = (key: string, params: Record<string, string | number> = {}) => t(`features.nodes.${key}`, params)
const route = useRoute(), router = useRouter()
const { workspace, commands } = useWorkspace()
const { date } = useFormat()
const task = useSimulatedTask()
const busy = computed(() => task.state.value === 'running')
const selected = ref<string[]>([])
const valueOf = (key: string) => { const value = route.query[key]; return typeof value === 'string' ? value : '' }
function updateQuery(key: string, value: string) {
  void router.replace(pageLocation('nodes', {}, { ...route.query, page: '1', [key]: value || undefined }))
}
const search = computed({ get: () => valueOf('q'), set: value => updateQuery('q', value) })
const mode = computed({ get: () => valueOf('mode'), set: value => updateQuery('mode', value) })
const location = computed({ get: () => valueOf('location'), set: value => updateQuery('location', value) })
const environment = computed({ get: () => valueOf('environment'), set: value => updateQuery('environment', value) })
const status = computed({ get: () => valueOf('status'), set: value => updateQuery('status', value) })
const sort = computed({ get: () => valueOf('sort') || 'name', set: value => updateQuery('sort', value) })
const statuses = ['online', 'offline', 'unknown', 'tunnel_offline', 'not_checked', 'failed', 'removed']
const locations = computed(() => [...new Set(workspace.nodes.map(node => node.location).filter(Boolean))].sort())
const hasFilters = computed(() => !!(search.value || mode.value || location.value || environment.value || status.value))
const filtered = computed(() => {
  const q = search.value.trim().toLocaleLowerCase()
  return workspace.nodes.filter(node =>
    (!q || [node.name, node.address, node.controlAddress, node.observedSourceIp].some(value => value?.toLocaleLowerCase().includes(q))) &&
    (!mode.value || node.mode === mode.value) && (!location.value || locationKey(node.location) === location.value) &&
    (!environment.value || node.environment === environment.value) && statusMatches(node, status.value)
  ).slice().sort((a, b) => {
    const result = sort.value.includes('updatedAt') ? a.updatedAt.localeCompare(b.updatedAt) : a.name.localeCompare(b.name)
    return (sort.value.startsWith('-') ? -result : result) || a.id.localeCompare(b.id)
  })
})
const pageSize = computed({ get: () => Math.min(9999, Math.max(1, Number(valueOf('pageSize')) || 10)), set: value => updateQuery('pageSize', String(value)) })
const page = computed({
  get: () => Math.min(Math.max(1, Math.ceil(filtered.value.length / pageSize.value)), Math.max(1, Number(valueOf('page')) || 1)),
  set: value => { void router.replace(pageLocation('nodes', {}, { ...route.query, page: String(value) })) },
})
const visible = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
const allSelected = computed(() => visible.value.length > 0 && visible.value.every(node => selected.value.includes(node.id)))
const someSelected = computed(() => !allSelected.value && visible.value.some(node => selected.value.includes(node.id)))
const drafts = computed(() => workspace.drafts.filter(draft => draft.feature === 'nodes').slice().sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)))
function select(id: string, checked: boolean) { selected.value = checked ? [...new Set([...selected.value, id])] : selected.value.filter(item => item !== id) }
function selectPage(checked: boolean) { visible.value.forEach(node => select(node.id, checked)) }
function clearFilters() { void router.replace(pageLocation('nodes', {}, { sort: valueOf('sort') || undefined, pageSize: valueOf('pageSize') || undefined })) }
function routeCount(id: string) { return workspace.routes.filter(item => item.draft.nodeIds.includes(id) || item.published?.nodeIds.includes(id)).length }
function snapshotExpiry(time?: string) { return time ? new Date(Date.parse(time) + (workspace.settings.measurementTtlHours ?? 96) * 3600000).toISOString() : undefined }

watch(() => route.query, () => {
  const invalid = [
    mode.value && !['direct', 'reverse'].includes(mode.value) ? 'mode' : '',
    environment.value && !environments.some(item => item === environment.value) ? 'environment' : '',
    status.value && !statuses.includes(status.value) ? 'status' : '',
  ].filter(Boolean)
  if (invalid.length) {
    const clean = { ...route.query }
    invalid.forEach(key => delete clean[key])
    void router.replace(pageLocation('nodes', {}, clean))
  }
}, { immediate: true })
watch(() => workspace.nodes.map(node => node.id), ids => { selected.value = selected.value.filter(id => ids.includes(id)) })

type BatchKind = 'status' | 'location'
type Result = { id: string; name: string; state: 'success' | 'skipped' | 'failed'; message: string }
const results = ref<Result[]>([])
const completedAt = ref('')
const batchKind = ref<BatchKind>('status')
const scopeCount = ref(0)
const cancelled = ref(false)
const summary = computed(() => ({
  success: results.value.filter(item => item.state === 'success').length,
  skipped: results.value.filter(item => item.state === 'skipped').length,
  failed: results.value.filter(item => item.state === 'failed').length,
}))
async function runQuery(kind: BatchKind, ids = selected.value.slice()) {
  if (busy.value || !ids.length) return
  batchKind.value = kind; scopeCount.value = ids.length; cancelled.value = false
  const generation = resetEpoch.value
  const completed = await task.run('success', 650)
  if (!completed || generation !== resetEpoch.value) return
  const now = new Date().toISOString()
  results.value = ids.map(id => {
    const original = workspace.nodes.find(node => node.id === id)
    if (!original) return { id, name: id, state: 'failed', message: 'missingFailure' }
    if (original.removedAt) return { id, name: original.name, state: 'failed', message: 'removedFailure' }
    const node: Node = JSON.parse(JSON.stringify(original))
    if (workspace.scenario === 'error' || (kind === 'location' && node.control !== 'online')) {
      if (kind === 'status') { node.queryIssue = { code: 'features.nodes.errors.QUERY_FAILED' }; commands.upsert('nodes', node) }
      return { id, name: node.name, state: 'failed', message: workspace.scenario === 'error' ? 'failed' : 'offlineFailure' }
    }
    if (kind === 'location') {
      if (node.manualLocation) return { id, name: node.name, state: 'skipped', message: 'preserved' }
      Object.assign(node, automaticLocation(node))
    } else {
      node.queriedAt = now
      if (node.control === 'online') node.lastConfirmedAt = now
      delete node.queryIssue
      // Read the existing demo connection state; this is not protocol authentication.
    }
    node.updatedAt = now
    commands.upsert('nodes', node)
    return { id, name: node.name, state: 'success', message: kind === 'status' ? 'successStatus' : 'successLocation' }
  })
  completedAt.value = now
}
function cancelQuery() { task.cancel(); cancelled.value = true }
function retry() { void runQuery(batchKind.value, results.value.filter(item => item.state === 'failed').map(item => item.id)) }
watch(resetEpoch, () => { task.cancel(); selected.value = []; results.value = []; completedAt.value = ''; cancelled.value = false })
</script>

<template>
  <div class="nodes-page">
    <PageHeader :title="tr('NodesPage.title')" :description="tr('NodesPage.goal')">
      <template #actions><NButton type="primary" @click="router.push(pageLocation('node-new', {}, { step: 'basics' }))">{{ tr('list.add') }}</NButton></template>
    </PageHeader>
    <section v-if="drafts.length" class="panel draft-panel" :aria-label="tr('list.drafts')">
      <h2>{{ tr('list.drafts') }}</h2>
      <ul class="draft-list"><li v-for="draft in drafts" :key="draft.id">
        <RouterLink :to="pageLocation('node-new', {}, { draftId: draft.id, step: draft.step || 'basics' })">{{ tr('list.resume', { name: draft.name || tr('list.untitled') }) }}</RouterLink>
        <span>{{ date(draft.updatedAt) }}</span>
        <NButton size="small" quaternary :aria-label="`${tr('list.deleteDraft')}: ${draft.name || tr('list.untitled')}`" @click="commands.remove('drafts', draft.id)">{{ tr('list.deleteDraft') }}</NButton>
      </li></ul>
    </section>
    <section class="panel filter-panel" :aria-label="tr('list.search')">
      <div class="filters">
        <div class="search"><label for="node-search">{{ tr('list.search') }}</label><NInput v-model:value="search" clearable :maxlength="120" :placeholder="tr('list.searchPlaceholder')" :input-props="{ id: 'node-search' }" /></div>
        <div><label for="node-mode">{{ tr('list.mode') }}</label><select id="node-mode" v-model="mode"><option value="">{{ tr('list.all') }}</option><option value="direct">{{ tr('mode.direct') }}</option><option value="reverse">{{ tr('mode.reverse') }}</option></select></div>
        <div><label for="node-location">{{ tr('list.location') }}</label><select id="node-location" v-model="location"><option value="">{{ tr('list.all') }}</option><option v-for="item in locations" :key="item" :value="locationKey(item)">{{ item }}</option></select></div>
        <div><label for="node-environment">{{ tr('list.environment') }}</label><select id="node-environment" v-model="environment"><option value="">{{ tr('list.all') }}</option><option v-for="item in environments" :key="item" :value="item">{{ tr(`environment.${item}`) }}</option></select></div>
        <div><label for="node-status">{{ tr('list.status') }}</label><select id="node-status" v-model="status"><option value="">{{ tr('list.all') }}</option><option v-for="item in statuses" :key="item" :value="item">{{ tr(`status.${item}`) }}</option></select></div>
      </div>
      <div class="filter-footer"><NButton :disabled="!hasFilters" @click="clearFilters">{{ tr('list.clear') }}</NButton><div class="sort"><label for="node-sort">{{ tr('list.sort') }}</label><select id="node-sort" v-model="sort"><option value="name">{{ tr('list.nameAsc') }}</option><option value="-name">{{ tr('list.nameDesc') }}</option><option value="-updatedAt">{{ tr('list.recent') }}</option><option value="updatedAt">{{ tr('list.oldest') }}</option></select></div></div>
    </section>
    <NAlert v-if="workspace.scenario === 'error'" type="warning" :show-icon="false" class="notice">{{ tr('list.demoError') }}</NAlert>
    <section class="panel node-table-panel">
      <div class="batch-actions">
        <strong>{{ tr('list.selected', { count: selected.length }) }}</strong>
        <NButton :disabled="!selected.length || busy" @click="runQuery('location')">{{ tr('list.updateLocation') }}</NButton>
        <NButton :disabled="!selected.length || busy" @click="runQuery('status')">{{ tr('list.queryStatus') }}</NButton>
        <NButton v-if="busy" @click="cancelQuery">{{ tr('list.cancel') }}</NButton>
      </div>
      <p class="hint" aria-live="polite">{{ busy ? tr('list.batchScope', { count: scopeCount }) : tr('list.selectionHint') }}</p>
      <div v-if="visible.length" class="table-scroll" tabindex="0" :aria-label="tr('NodesPage.title')" :aria-busy="busy">
        <table><thead><tr>
          <th class="check-cell"><NCheckbox :checked="allSelected" :indeterminate="someSelected" :disabled="busy" :aria-label="tr('list.selectPage')" @update:checked="selectPage" /></th>
          <th scope="col">{{ tr('list.name') }}</th><th scope="col">{{ tr('list.mode') }}</th><th scope="col">{{ tr('list.address') }}</th><th scope="col">{{ tr('list.connection') }}</th><th scope="col">{{ tr('list.routes') }}</th><th scope="col">{{ tr('list.lastConfirmed') }}</th><th scope="col">{{ tr('list.versions') }}</th>
        </tr></thead><tbody><tr v-for="node in visible" :key="node.id">
          <td class="check-cell"><NCheckbox :checked="selected.includes(node.id)" :disabled="busy" :aria-label="tr('list.select', { name: node.name })" @update:checked="checked => select(node.id, checked)" /></td>
          <td><RouterLink class="node-name" :to="entityLocation('node', node.id)">{{ node.name }}</RouterLink><small>{{ [node.location, node.city].filter(Boolean).join(' · ') || tr('list.unknown') }}</small><small v-if="node.manualLocation">{{ tr('list.manual') }}</small><small v-if="node.removedAt" class="failure">{{ tr('list.removed') }}</small><div v-if="node.tags?.length" class="tags"><span v-for="tag in node.tags" :key="tag">{{ tag }}</span></div></td>
          <td>{{ tr(`mode.${node.mode}`) }}<small>{{ tr(`environment.${node.environment || 'unknown'}`) }}</small></td>
          <td><code v-if="node.mode === 'direct'">{{ node.address || tr('list.unknown') }}</code><span v-else>{{ tr('list.noAddress') }}</span><small>{{ tr('list.controlSource', { address: node.observedSourceIp || node.controlAddress || tr('list.unknown') }) }}</small></td>
          <td><span :class="['connection-state', node.control]">{{ tr('list.control', { state: tr(`status.${node.control}`) }) }}</span><small v-if="node.mode === 'reverse'">{{ tr('list.tunnel', { state: tr(`status.${node.tunnel}`) }) }}</small><small>{{ tr('list.verification', { state: tr(`status.${node.verification}`) }) }}</small><small v-if="node.queryIssue" class="failure">{{ tr('list.failureSnapshot') }}</small></td>
          <td><RouterLink :to="pageLocation('routes', {}, { nodeId: node.id })">{{ tr('list.routeCount', { count: routeCount(node.id) }) }}</RouterLink></td>
          <td><span>{{ node.lastConfirmedAt ? date(node.lastConfirmedAt) : tr('list.notQueried') }}</span><QueryStamp :queried-at="node.queriedAt" :expires-at="snapshotExpiry(node.queriedAt)" /><small v-if="workspace.scenario === 'stale' && !node.queriedAt">{{ tr('list.stale') }}</small></td>
          <td><code>{{ node.agentVersion || tr('list.unknown') }}</code><small><code>{{ node.mihomoVersion || tr('list.unknown') }}</code></small></td>
        </tr></tbody></table>
      </div>
      <EmptyState v-else :title="hasFilters ? tr('list.filteredTitle') : tr('list.emptyTitle')" :description="hasFilters ? tr('list.filteredBody') : tr('list.emptyBody')" :filtered="hasFilters" :action-label="tr('list.add')" @clear="clearFilters" @action="router.push(pageLocation('node-new', {}, { step: 'basics' }))" />
      <div v-if="filtered.length" class="pagination"><span>{{ tr('list.count', { count: filtered.length }) }}</span><NPagination v-model:page="page" :page-count="Math.max(1, Math.ceil(filtered.length / pageSize))" :page-slot="5" :aria-label="tr('list.page')" /><label class="page-size">{{ tr('list.pageSize') }}<select v-model.number="pageSize"><option v-for="size in [...new Set([10, 20, 50, pageSize])].sort((a, b) => a - b)" :key="size" :value="size">{{ size }}</option></select></label></div>
    </section>
    <NAlert v-if="cancelled" type="info" :show-icon="false" role="status" class="notice">{{ tr('list.cancelled') }}</NAlert>
    <section v-if="results.length" class="panel results" aria-live="polite">
      <h2>{{ tr('list.results') }}</h2><p>{{ tr('list.resultSummary', summary) }}</p><p class="hint">{{ tr('list.resultTime', { time: date(completedAt) }) }}</p>
      <ul><li v-for="result in results" :key="result.id"><RouterLink v-if="workspace.nodes.some(node => node.id === result.id)" :to="entityLocation('node', result.id)">{{ result.name }}</RouterLink><span v-else>{{ result.name }}</span><span :class="{ failure: result.state === 'failed' }">{{ tr(`list.${result.message}`) }}</span></li></ul>
      <NButton v-if="summary.failed" :disabled="busy" @click="retry">{{ tr('list.retry') }}</NButton>
    </section>
  </div>
</template>

<style scoped>
.filters { display:grid; grid-template-columns: minmax(220px,2fr) repeat(4,minmax(125px,1fr)); gap:16px; }
select { width:100%; min-height:36px; border:1px solid var(--color-border); border-radius:4px; background:white; color:var(--color-text); padding:7px 10px; font:inherit; }
.filter-footer,.batch-actions,.pagination { display:flex; align-items:center; flex-wrap:wrap; gap:12px; }
.filter-footer { justify-content:space-between; margin-top:20px; }
.sort { display:flex; align-items:center; gap:12px; }.sort label { margin:0; white-space:nowrap; }
.node-table-panel { padding:24px 0; }.batch-actions,.hint,.pagination { margin-left:24px; margin-right:24px; }.hint { font-size:12px; }
table { width:100%; border-collapse:collapse; min-width:1120px; text-align:left; }th,td { padding:16px 12px; border-bottom:1px solid var(--color-border); vertical-align:top; }th { font-size:12px; color:var(--color-muted); background:#f7faf7; }th:first-child,td:first-child { padding-left:24px; }.check-cell { width:44px; }
.node-name { font-weight:600; }small { display:block; color:var(--color-muted); margin-top:6px; line-height:1.6; }code { font-family:ui-monospace,monospace; overflow-wrap:anywhere; }
.connection-state { display:inline-block; padding:3px 8px; border-radius:5px; background:#f1f3f0; font-size:12px; }.connection-state.online { color:#206553; background:#eaf3ed; }.connection-state.offline,.failure { color:#a1372d; }
.pagination { justify-content:space-between; margin-top:24px; }.page-size { display:flex; align-items:center; gap:8px; margin:0; }.page-size select { width:80px; }
.notice { margin-bottom:24px; }.tags { display:flex; flex-wrap:wrap; gap:4px; margin-top:8px; }.tags span { padding:2px 5px; background:#f1f3f0; border-radius:4px; font-size:11px; }
.draft-panel { padding:20px 24px; }.draft-list { list-style:none; margin:0; padding:0; }.draft-list li { display:flex; align-items:center; flex-wrap:wrap; gap:16px; }.draft-list span { color:var(--color-muted); font-size:12px; }.draft-list a { padding:12px 0; }
.results ul { padding:0; list-style:none; }.results li { display:flex; gap:16px; align-items:baseline; padding:12px 0; border-top:1px solid var(--color-border); }.results li a { min-width:120px; }.results .hint { margin-left:0; }
@media(max-width:1150px) { .filters { grid-template-columns:repeat(2,minmax(0,1fr)); }.search { grid-column:1/-1; } }
@media(max-width:600px) { .filters { grid-template-columns:1fr; }.filter-panel { padding:20px 16px; }.batch-actions,.hint,.pagination { margin-left:16px; margin-right:16px; }.sort { width:100%; }select { min-height:44px; }.batch-actions :deep(button) { min-height:44px; }.results li { flex-direction:column; gap:4px; }.pagination { align-items:flex-start; }.pagination :deep(.n-pagination) { flex-wrap:wrap; }.draft-list li { gap:4px 12px; } }
</style>
