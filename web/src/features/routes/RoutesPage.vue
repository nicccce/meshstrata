<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NInput, NSelect, NButton, NPagination, NAlert } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import EntityLink from '../../components/EntityLink.vue'
import RoutePath from '../../components/RoutePath.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import EmptyState from '../../components/EmptyState.vue'
import QueryStamp from '../../components/QueryStamp.vue'
import TaskFeedback from '../../components/TaskFeedback.vue'
import { resetEpoch, useWorkspace } from '../../demo/workspace'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import { pageLocation } from '../../app/router'
import DuplicateRouteDialog from './DuplicateRouteDialog.vue'
import { configState, displayedRevision, nodeName, pathNodes, protocols, referencedSubscriptions, routeProtocols, savedRouteDraft, states, udpCapability, udpStates, type RouteSnapshot } from './route-model'
const { t } = useI18n(); const current = useRoute(); const router = useRouter(); const { workspace } = useWorkspace()
const { state: queryState, run } = useSimulatedTask()
const copySource = ref<RouteSnapshot>(); const copyId = ref('')
const prefix = 'features.routes.'
function query(key: string) { const value = current.query[key]; return typeof value === 'string' ? value : '' }
function setFilter(key: string, value: string | null) { void router.replace(pageLocation('routes', {}, { ...current.query, [key]: value || undefined, page: '1' })) }
function field(key: string) { return computed({ get: () => query(key) || null, set: (value: string | null) => setFilter(key, value) }) }
const search = computed({ get: () => query('q'), set: value => setFilter('q', value.slice(0, 120)) })
const nodeFilter = field('nodeId'); const exitFilter = field('exitNodeId'); const protocolFilter = field('protocol'); const statusFilter = field('status'); const udpFilter = field('udp')
const sort = computed({ get: () => query('sort') || '-updatedAt', set: value => setFilter('sort', value) })
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  const rows = workspace.routes.filter(route => {
    const revision = displayedRevision(route)
    const terminal = revision.nodeIds.at(-1)
    const policy = workspace.policies.find(item => item.id === revision.policyId)
    return (!q || [route.name, route.id, policy?.name ?? '', ...revision.nodeIds.map(id => nodeName(workspace, id))].some(value => value.toLowerCase().includes(q))) &&
      (!nodeFilter.value || revision.nodeIds.includes(nodeFilter.value)) && (!exitFilter.value || terminal === exitFilter.value) &&
      (!protocolFilter.value || routeProtocols(workspace, revision).some(protocol => protocol === protocolFilter.value)) &&
      (!statusFilter.value || configState(route) === statusFilter.value) && (!udpFilter.value || udpCapability(workspace, revision) === udpFilter.value)
  })
  return [...rows].sort((a, b) => {
    const key = sort.value.includes('name') ? 'name' : 'updatedAt'
    const order = a[key].localeCompare(b[key]) || a.id.localeCompare(b.id)
    return sort.value.startsWith('-') ? -order : order
  })
})
const pageSize = computed(() => Math.min(100, Math.max(1, Number(query('pageSize')) || 10)))
const page = computed(() => Math.min(Math.max(1, Number(query('page')) || 1), Math.max(1, Math.ceil(filtered.value.length / pageSize.value))))
const visible = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
const hasFilters = computed(() => [search.value, nodeFilter.value, exitFilter.value, protocolFilter.value, statusFilter.value, udpFilter.value].some(Boolean))
const nodeOptions = computed(() => workspace.nodes.map(node => ({ value: node.id, label: node.name })))
const protocolOptions = computed(() => protocols.map(value => ({ value, label: t(`${prefix}protocols.${value}`) })))
const statusOptions = computed(() => states.map(value => ({ value, label: t(`${prefix}states.${value}`) })))
const udpOptions = computed(() => udpStates.map(value => ({ value, label: t(`${prefix}udpStates.${value}`) })))
const sortOptions = computed(() => ['-updatedAt', 'updatedAt', 'name', '-name'].map(value => ({ value, label: t(`${prefix}sorts.${value}`) })))
const invalidFilters = computed(() => [nodeFilter.value, exitFilter.value].filter((value): value is string => !!value).filter(value => !workspace.nodes.some(node => node.id === value)).concat(protocolFilter.value && !protocols.some(value => value === protocolFilter.value) ? [protocolFilter.value] : [], statusFilter.value && !states.some(value => value === statusFilter.value) ? [statusFilter.value] : [], udpFilter.value && !udpStates.some(value => value === udpFilter.value) ? [udpFilter.value] : []))
function clearFilters() { void router.replace(pageLocation('routes')) }
function duplicate(source: RouteSnapshot) { copyId.value = `route-copy-${crypto.randomUUID()}`; copySource.value = source }
function egressLabel(route: RouteSnapshot) {
  const revision = displayedRevision(route)
  if (!revision.policyId) return t(`${prefix}nodeDefault`)
  const policy = workspace.policies.find(item => item.id === revision.policyId && item.revision === revision.policyRevision)
  return policy ? `${policy.name} · r${policy.revision}` : t(`${prefix}missingPolicy`, { id: revision.policyId, revision: revision.policyRevision ?? '?' })
}
watch(resetEpoch, () => { copySource.value = undefined })
</script>
<template>
  <div class="routes-page">
    <PageHeader :title="t(`${prefix}RoutesPage.title`)" :description="t(`${prefix}RoutesPage.goal`)"><template #actions><NButton type="primary" @click="router.push(pageLocation('route-new', {}, { step: 'exit' }))">{{ t(`${prefix}create`) }}</NButton></template></PageHeader>
    <section class="panel filters" :aria-label="t(`${prefix}filters`)">
      <label class="search">{{ t(`${prefix}search`) }}<NInput v-model:value="search" clearable :placeholder="t(`${prefix}searchHint`)" :input-props="{ 'aria-label': t(`${prefix}search`) }" /></label>
      <label>{{ t(`${prefix}node`) }}<NSelect v-model:value="nodeFilter" clearable :options="nodeOptions" :placeholder="t(`${prefix}allNodes`)" :aria-label="t(`${prefix}node`)" /></label>
      <label>{{ t(`${prefix}terminal`) }}<NSelect v-model:value="exitFilter" clearable :options="nodeOptions" :placeholder="t(`${prefix}allExits`)" :aria-label="t(`${prefix}terminal`)" /></label>
      <label>{{ t(`${prefix}protocol`) }}<NSelect v-model:value="protocolFilter" clearable :options="protocolOptions" :placeholder="t(`${prefix}allProtocols`)" :aria-label="t(`${prefix}protocol`)" /></label>
      <label>{{ t(`${prefix}configuration`) }}<NSelect v-model:value="statusFilter" clearable :options="statusOptions" :placeholder="t(`${prefix}allStates`)" :aria-label="t(`${prefix}configuration`)" /></label>
      <label>{{ t(`${prefix}businessUdp`) }}<NSelect v-model:value="udpFilter" clearable :options="udpOptions" :placeholder="t(`${prefix}allCapabilities`)" :aria-label="t(`${prefix}businessUdp`)" /></label>
      <label>{{ t(`${prefix}sort`) }}<NSelect v-model:value="sort" :options="sortOptions" :aria-label="t(`${prefix}sort`)" /></label>
      <NButton :disabled="!hasFilters" @click="clearFilters">{{ t('common.clear') }}</NButton>
    </section>
    <NAlert v-if="invalidFilters.length" type="warning" :bordered="false">{{ t(`${prefix}invalidFilters`, { values: invalidFilters.join(', ') }) }}</NAlert>
    <NAlert v-if="workspace.scenario === 'error'" type="error" :bordered="false"><p>{{ t(`${prefix}queryError`) }}</p><NButton :loading="queryState === 'running'" @click="run('error')">{{ t('common.retry') }}</NButton><TaskFeedback v-if="queryState !== 'idle'" :state="queryState" /></NAlert>
    <p class="muted" aria-live="polite">{{ t(`${prefix}resultCount`, { count: filtered.length, total: workspace.routes.length }) }} · {{ t(`${prefix}snapshotHint`) }}</p>
    <EmptyState v-if="!workspace.routes.length" :title="t(`${prefix}emptyTitle`)" :description="t(`${prefix}emptyBody`)" :action-label="t(`${prefix}create`)" @action="router.push(pageLocation('route-new', {}, { step: 'exit' }))" />
    <EmptyState v-else-if="!filtered.length" filtered :title="t(`${prefix}noResults`)" :description="t(`${prefix}noResultsBody`)" @clear="clearFilters" />
    <template v-else>
      <div class="panel table-scroll" tabindex="0" :aria-label="t(`${prefix}tableLabel`)">
        <table><thead><tr><th>{{ t(`${prefix}name`) }}</th><th>{{ t(`${prefix}fixedPath`) }}</th><th>{{ t(`${prefix}terminalEgress`) }}</th><th>{{ t(`${prefix}configuration`) }}</th><th>{{ t(`${prefix}capability`) }}</th><th>{{ t(`${prefix}verification`) }}</th><th>{{ t(`${prefix}subscriptions`) }}</th><th>{{ t(`${prefix}actions`) }}</th></tr></thead>
          <tbody><tr v-for="item in visible" :key="item.id">
            <td><EntityLink kind="route" :id="item.id" :label="item.name" /><small>{{ item.id }}</small></td>
            <td><RoutePath :nodes="pathNodes(workspace, displayedRevision(item))" /><small>{{ routeProtocols(workspace, displayedRevision(item)).map(protocol => t(`${prefix}protocols.${protocol}`)).join(' · ') }}</small></td>
            <td><EntityLink v-if="displayedRevision(item).nodeIds.at(-1)" kind="node" :id="displayedRevision(item).nodeIds.at(-1)!" :label="nodeName(workspace, displayedRevision(item).nodeIds.at(-1)!)" /><small>{{ egressLabel(item) }}</small></td>
            <td><StatusBadge :status="configState(item)" :label="t(`${prefix}states.${configState(item)}`)" /><small>{{ item.published ? t(`${prefix}publishedRevision`, { revision: item.published.revision }) : t(`${prefix}unpublished`) }}</small></td>
            <td><span>{{ t(`${prefix}tcpSummary`) }}</span><small>{{ t(`${prefix}businessUdp`) }}: {{ t(`${prefix}udpStates.${udpCapability(workspace, displayedRevision(item))}`) }}</small></td>
            <td><StatusBadge :status="item.verification" /><QueryStamp :queried-at="item.verifiedAt" /></td>
            <td><RouterLink :to="pageLocation('subscriptions', {}, { routeId: item.id })">{{ referencedSubscriptions(workspace, item.id).length }}</RouterLink></td>
            <td><div class="row-actions"><NButton size="small" @click="router.push(pageLocation('route-detail', { routeId: item.id }))">{{ t(`${prefix}view`) }}</NButton><NButton size="small" @click="router.push(pageLocation('route-edit', { routeId: item.id }, { step: 'options', draftId: savedRouteDraft(workspace, item.id)?.id }))">{{ t(`${prefix}edit`) }}</NButton><NButton size="small" @click="duplicate(item)">{{ t(`${prefix}duplicate`) }}</NButton></div></td>
          </tr></tbody>
        </table>
      </div>
      <div class="pagination"><NPagination :page="page" :page-size="pageSize" :item-count="filtered.length" :page-sizes="[10, 20, 50]" show-size-picker @update:page="value => router.replace(pageLocation('routes', {}, { ...current.query, page: String(value) }))" @update:page-size="value => router.replace(pageLocation('routes', {}, { ...current.query, page: '1', pageSize: String(value) }))" /></div>
    </template>
    <DuplicateRouteDialog v-if="copySource" :key="copyId" :source="copySource" :draft-id="copyId" @close="copySource = undefined" />
  </div>
</template>
<style scoped>
.routes-page { display: grid; gap: 16px; }.filters { display: grid; grid-template-columns: repeat(4, minmax(150px, 1fr)); gap: 14px; align-items: end; }.filters label { display: grid; gap: 6px; font-size: 13px; font-weight: 500; }.search { grid-column: span 2; }table { border-collapse: collapse; width: 100%; min-width: 1130px; }th, td { text-align: left; vertical-align: top; padding: 16px 12px; border-bottom: 1px solid #e1e9e4; }th { font-size: 12px; color: #586c61; background: #f6faf7; }td small { display: block; margin-top: 8px; color: #63736a; }.row-actions { display: grid; gap: 6px; }.row-actions :deep(button) { min-height: 36px; }.pagination { display: flex; justify-content: flex-end; overflow-x: auto; padding: 4px; }.muted { color: #63736a; margin: 0; font-size: 13px; }.table-scroll { padding: 0; overflow-x: auto; }@media (max-width: 900px) { .filters { grid-template-columns: repeat(2, minmax(0, 1fr)); } }@media (max-width: 480px) { .filters { grid-template-columns: 1fr; }.search { grid-column: auto; }.row-actions :deep(button) { min-height: 44px; } }
</style>
