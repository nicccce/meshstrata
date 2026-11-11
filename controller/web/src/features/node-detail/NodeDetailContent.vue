<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NAlert, useMessage } from 'naive-ui'
import { pageLocation, entityLocation } from '../../app/router'
import { useFormat } from '../../composables/useFormat'
import PageHeader from '../../components/PageHeader.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import QueryStamp from '../../components/QueryStamp.vue'
import ErrorState from '../../components/ErrorState.vue'
import ConfirmImpactDialog from '../../components/ConfirmImpactDialog.vue'
import TaskFeedback from '../../components/TaskFeedback.vue'
import type { Issue, Listener, ReverseTunnel, Subscription, Release } from '../../domain'
import { cloneNode, cloneRoute } from './model'
import { useNodeContext, useNodeTask } from './useNodeContext'
import ProtocolPanel from './components/ProtocolPanel.vue'
import DnsPanel from './components/DnsPanel.vue'
import ConnectivityPanel from './components/ConnectivityPanel.vue'
import NodeMetadataEditor from './components/NodeMetadataEditor.vue'
import RelatedRoutes from './components/RelatedRoutes.vue'

const props = defineProps<{ nodeId: string }>()
const { node, routes, listeners, tunnels, workspace, commands } = useNodeContext(props.nodeId)
const { t } = useI18n()
const { date, number } = useFormat()
const route = useRoute()
const key = 'features.node-detail.'
const tab = computed(() => typeof route.query.tab === 'string' ? route.query.tab : 'overview')
const tabs = ['overview', 'protocols', 'dns', 'connectivity', 'routes']
const queryTask = useNodeTask()
const removeTask = useNodeTask()
const locationTask = useNodeTask()
const queryError = ref<Issue>()
const showEdit = ref(false)
const showRemove = ref(false)
const message = useMessage()
const linkedSubscriptions = computed(() => workspace.subscriptions.filter(s => s.routeIds.some(id => routes.value.some(r => r.id === id)) || s.entryIds.some(id => workspace.entries.some(e => e.id === id && listeners.value.some(l => l.id === e.listenerId)))))
const verificationAt = computed(() => routes.value.map(r => r.verifiedAt).filter((at): at is string => Boolean(at)).sort().at(-1))
const summaryStates = computed(() => [
  { label: 'control', state: node.value.control, at: node.value.lastConfirmedAt },
  { label: 'tunnel', state: node.value.mode === 'reverse' ? node.value.tunnel : 'unknown', at: undefined },
  { label: 'verification', state: node.value.verification, at: verificationAt.value },
])
const impacts = computed(() => [
  t(key + 'removeRoutes', { names: routes.value.map(r => r.name).join(', ') || t(key + 'none') }),
  t(key + 'removeSubscriptions', { names: linkedSubscriptions.value.map(s => s.name).join(', ') || t(key + 'none') }),
  t(key + 'removeIdentity'), t(key + 'removeTunnels', { count: tunnels.value.length }),
  t(key + 'removeCleanup', { count: listeners.value.length }),
])
const expiry = computed(() => workspace.scenario === 'stale' && node.value.queriedAt ? '2020-01-01T00:00:00Z' : node.value.queriedAt ? new Date(Date.parse(node.value.queriedAt) + (workspace.settings.measurementTtlHours ?? 96) * 3600000).toISOString() : undefined)
async function queryStatus() {
  if (queryTask.state.value === 'running') return
  queryError.value = undefined
  const success = await queryTask.run(workspace.scenario === 'error' || node.value.control !== 'online' ? 'error' : 'success')
  if (!success) { if (queryTask.state.value === 'error') queryError.value = { code: node.value.control !== 'online' ? 'NODE_OFFLINE' : 'DEMO_ERROR' }; return }
  const next = cloneNode(node.value)
  const now = new Date().toISOString()
  next.queriedAt = now; next.lastConfirmedAt = now; next.updatedAt = now; next.queryIssue = undefined
  // Stable demonstration samples; unknown values remain unknown in the partial scenario.
  next.resources = { cpuPercent: workspace.scenario === 'partial' ? null : 12.4, memoryBytes: 268435456, diskBytes: workspace.scenario === 'partial' ? null : 2147483648, networkBytesPerSecond: workspace.scenario === 'partial' ? null : 18432 }
  commands.upsert('nodes', next)
  message.success(t(key + 'snapshotDone'))
}
async function updateLocation() {
  if (locationTask.state.value === 'running') return
  if (!await locationTask.run(workspace.scenario === 'error' || node.value.control !== 'online' ? 'error' : 'success')) return
  const next = cloneNode(node.value)
  if (next.manualLocation) { message.info(t(key + 'manualLocationKept')); return }
  const locations: Record<string, string> = { hk: 'HK', sg: 'SG', jp: 'JP', relay: 'Local', reverse: 'Remote' }
  next.location = locations[next.id] ?? 'Demo region'
  next.updatedAt = new Date().toISOString()
  commands.upsert('nodes', next)
  message.success(t(key + 'locationDone'))
}
async function removeNode() {
  if (removeTask.state.value === 'running' || node.value.removedAt) return
  if (!await removeTask.run(workspace.scenario === 'error' ? 'error' : 'success')) return
  const now = new Date().toISOString()
  const next = cloneNode(node.value)
  next.removedAt = now; next.control = 'offline'; next.tunnel = next.mode === 'reverse' ? 'offline' : next.tunnel; next.verification = 'incomplete'; next.updatedAt = now
  commands.upsert('nodes', next)
  routes.value.forEach(original => { const route = cloneRoute(original); route.cleanup = 'pending'; route.verification = 'incomplete'; route.draft.validated = false; route.updatedAt = now; commands.upsert('routes', route) })
  listeners.value.forEach(original => { const listener: Listener = JSON.parse(JSON.stringify(original)); listener.cleanup = 'pending'; listener.updatedAt = now; commands.upsert('listeners', listener) })
  tunnels.value.forEach(original => { const tunnel: ReverseTunnel = JSON.parse(JSON.stringify(original)); tunnel.relayApplied = false; tunnel.remoteApplied = false; tunnel.updatedAt = now; commands.upsert('tunnels', tunnel) })
  linkedSubscriptions.value.forEach(original => { const subscription: Subscription = JSON.parse(JSON.stringify(original)); subscription.sync = 'pending'; subscription.updatedAt = now; commands.upsert('subscriptions', subscription) })
  workspace.entries.filter(entry => routes.value.some(route => route.id === entry.routeId)).forEach(entry => commands.upsert('entries', { ...entry, cleanup: 'pending', updatedAt: now }))
  const release: Release = { id: `remove-${next.id}-${Date.now()}`, name: t(key + 'removalRelease', { name: next.name }), kind: 'revoke', nodeId: next.id, state: 'applying', revision: next.appliedRevision ?? 1, cleanup: 'pending', startedAt: now, updatedAt: now,
    nodes: [{ nodeId: next.id, applied: false, verified: false, rollback: 'none', issue: { code: 'NODE_OFFLINE' } }],
    stages: [{ id: 'identity', name: t(key + 'identityRevoked'), nodeId: next.id, state: 'succeeded', completedAt: now }, { id: 'cleanup', name: t(key + 'remoteCleanup'), nodeId: next.id, state: 'pending' }] }
  commands.upsert('releases', release)
  commands.upsert('events', { id: `event-${release.id}`, name: release.name, kind: 'node_removed', entityType: 'node', entityId: next.id, status: 'pending', releaseId: release.id, updatedAt: now })
  showRemove.value = false
  message.warning(t(key + 'removedNotice'))
}
function quantity(value: number | null | undefined, unit: 'MiB' | 'KiB/s') { return value == null ? t('common.unknown') : `${number(Math.round(value / (unit === 'MiB' ? 1048576 : 1024) * 10) / 10)} ${unit}` }
const resources = computed(() => [
  { label: 'cpu', value: node.value.resources?.cpuPercent == null ? t('common.unknown') : `${number(node.value.resources.cpuPercent)}%` },
  { label: 'memory', value: quantity(node.value.resources?.memoryBytes, 'MiB') },
  { label: 'disk', value: quantity(node.value.resources?.diskBytes, 'MiB') },
  { label: 'network', value: quantity(node.value.resources?.networkBytesPerSecond, 'KiB/s') },
])
</script>

<template>
  <div>
    <PageHeader :title="node.name" :description="`${node.location}${node.city ? ' / ' + node.city : ''} · ${t(key + node.mode)}`">
      <template #actions><NButton type="primary" :disabled="!!node.removedAt" :loading="queryTask.state.value === 'running'" @click="queryStatus">{{ t(key + 'queryStatus') }}</NButton></template>
    </PageHeader>
    <NAlert v-if="node.removedAt" type="warning" :title="t(key + 'removed')">{{ t(key + 'removedNotice') }} <RouterLink :to="pageLocation('activity', {}, { tab: 'releases', entityType: 'node', entityId: nodeId })">{{ t(key + 'releases') }}</RouterLink></NAlert>
    <div class="actions secondary">
      <NButton :disabled="!!node.removedAt" @click="showEdit = true">{{ t(key + 'edit') }}</NButton>
      <NButton :disabled="!!node.removedAt" :loading="locationTask.state.value === 'running'" @click="updateLocation">{{ t(key + 'updateLocation') }}</NButton>
      <NButton :disabled="!!node.removedAt" type="error" secondary @click="showRemove = true">{{ t(key + 'remove') }}</NButton>
      <RouterLink :to="pageLocation('nodes')">{{ t('common.back') }}</RouterLink>
    </div>
    <p v-if="locationTask.state.value === 'error'" role="alert">{{ t(key + 'locationFailed') }}</p>
    <ErrorState v-if="queryError || node.queryIssue" :issue="queryError ?? node.queryIssue!" retryable @retry="queryStatus" />
    <TaskFeedback v-if="queryTask.state.value === 'running' || queryTask.state.value === 'cancelled'" :state="queryTask.state.value" @cancel="queryTask.cancel" />
    <section class="state-grid" :aria-label="t(key + 'connectionStates')">
      <article v-for="state in summaryStates" :key="state.label" class="state-item">
        <h2>{{ t(key + state.label) }}</h2>
        <StatusBadge :status="state.state" :label="state.label === 'tunnel' && node.mode === 'direct' ? t(key + 'notApplicable') : undefined" />
        <p>{{ t(key + 'observedAt') }}: {{ date(state.at) }}</p>
      </article>
    </section>
    <nav class="tabs" :aria-label="t(key + 'tabsLabel')">
      <RouterLink v-for="name in tabs" :key="name" :to="pageLocation('node-detail', { nodeId }, { tab: name })" :aria-current="tab === name ? 'page' : undefined" :class="{ active: tab === name }">{{ t(key + 'tabs.' + name) }}</RouterLink>
    </nav>
    <section v-show="tab === 'overview'" class="panel">
      <h2>{{ t(key + 'instance') }}</h2>
      <dl class="details">
        <div><dt>{{ t(key + 'proxyAddress') }}</dt><dd>{{ node.mode === 'reverse' ? t(key + 'reverseAddress') : node.address || t('common.unknown') }}</dd></div>
        <div><dt>{{ t(key + 'controlAddress') }}</dt><dd>{{ node.controlAddress }}</dd></div>
        <div><dt>{{ t(key + 'sourceIp') }}</dt><dd>{{ node.observedSourceIp || t('common.unknown') }}</dd></div>
        <div><dt>{{ t(key + 'lastConfirmed') }}</dt><dd>{{ date(node.lastConfirmedAt) }}</dd></div>
        <div><dt>{{ t(key + 'agentVersion') }}</dt><dd>{{ node.agentVersion || t('common.unknown') }}</dd></div>
        <div><dt>{{ t(key + 'mihomoVersion') }}</dt><dd>{{ node.mihomoVersion || t('common.unknown') }}</dd></div>
        <div><dt>{{ t(key + 'appliedRevision') }}</dt><dd>{{ node.appliedRevision ?? t('common.unknown') }}</dd></div>
      </dl>
      <h2>{{ t(key + 'resources') }}</h2>
      <p>{{ t(key + 'resourceHint') }}</p>
      <QueryStamp :queried-at="node.queriedAt" :expires-at="expiry" :scope="node.name" />
      <dl class="details metrics"><div v-for="resource in resources" :key="resource.label"><dt>{{ t(key + resource.label) }}</dt><dd>{{ resource.value }}</dd></div></dl>
    </section>
    <ProtocolPanel v-show="tab === 'protocols'" :node-id="nodeId" />
    <DnsPanel v-show="tab === 'dns'" :node-id="nodeId" />
    <ConnectivityPanel v-show="tab === 'connectivity'" :node-id="nodeId" />
    <RelatedRoutes v-show="tab === 'routes'" :node-id="nodeId" />
    <NodeMetadataEditor v-model:show="showEdit" :node-id="nodeId" />
    <ConfirmImpactDialog v-model:show="showRemove" :title="t(key + 'removeTitle', { name: node.name })" :impacts="impacts" :busy="removeTask.state.value === 'running'" @confirm="removeNode" />
    <p v-if="removeTask.state.value === 'error'" role="alert">{{ t(key + 'removeFailed') }}</p>
  </div>
</template>

<style scoped>
.secondary { margin-bottom: 24px; }
.state-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); border: 1px solid var(--color-border); border-radius: var(--radius); background: white; margin: 24px 0; }
.state-item { padding: 20px; min-width: 0; }
.state-item + .state-item { border-left: 1px solid var(--color-border); }
.state-item h2 { font-size: 14px; margin-bottom: 12px; }
.state-item p { color: var(--color-muted); font-size: 12px; margin-bottom: 0; }
.tabs { display: flex; flex-wrap: wrap; gap: 8px; margin: 24px 0; }
.tabs a { display: inline-flex; align-items: center; min-height: 44px; padding: 10px 16px; text-decoration: none; border-radius: 8px; color: var(--color-muted); }
.tabs .active { background: #e6f1eb; color: var(--color-primary); font-weight: 600; }
.details { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 24px; margin: 24px 0 32px; }
.details div { min-width: 0; }
.metrics { grid-template-columns: repeat(4, minmax(0, 1fr)); margin-bottom: 0; }
.metrics dd { font-size: 20px; }
@media (max-width: 760px) { .state-grid, .details, .metrics { grid-template-columns: 1fr; } .state-item + .state-item { border-left: 0; border-top: 1px solid var(--color-border); } }
</style>
