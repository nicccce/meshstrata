<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { DeepReadonly } from 'vue'
import { NButton, NSelect, NAlert } from 'naive-ui'
import { useDisplay } from '../../composables/useDisplay'
import { useI18n } from 'vue-i18n'
import type { Node, UsageSnapshot, Measurement } from '../../domain'
import { useWorkspace, resetEpoch } from '../../demo/workspace'
import { useQueryState } from '../../composables/useQueryState'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import { useFormat } from '../../composables/useFormat'
import { entityLocation, pageLocation } from '../../app/router'
import QueryStamp from '../../components/QueryStamp.vue'
import ErrorState from '../../components/ErrorState.vue'
import { observationExpired } from './model'

const { display } = useDisplay()
const { t, locale } = useI18n()
const { date, number } = useFormat()
const { workspace } = useWorkspace()
type Kind = 'resources' | 'usage' | 'measurements'
interface Snapshot {
  kind: Kind; targetId: string; name: string
  node?: DeepReadonly<Node>; usage: DeepReadonly<UsageSnapshot>[]; measurements: DeepReadonly<Measurement>[]
}
const initialUsage = [...workspace.usage]
  .filter(row => workspace.subscriptions.some(subscription => subscription.id === row.subscriptionId))
  .sort((a, b) => Date.parse(b.queriedAt) - Date.parse(a.queriedAt))[0]
const kind = ref<Kind>(initialUsage ? 'usage' : 'resources')
const targetId = ref<string | null>(initialUsage?.subscriptionId ?? workspace.nodes.find(node => !node.removedAt)?.id ?? null)
const query = useQueryState<Snapshot>()
const task = useSimulatedTask()
const feedback = ref<'read' | 'cancelled'>()
const now = ref(Date.now())
const kinds = computed(() => (['resources', 'usage', 'measurements'] as const).map(value => ({ value, label: t(`features.overview.snapshots.${value}`) })))
const options = computed(() => {
  const entities = kind.value === 'resources' ? workspace.nodes.filter(node => !node.removedAt) : kind.value === 'usage' ? workspace.subscriptions : workspace.routes.filter(route => !route.withdrawnAt)
  return entities.map(entity => ({ label: `${entity.name} · ${entity.id}`, value: entity.id }))
})
function read(): Snapshot | undefined {
  const id = targetId.value
  if (!id) return undefined
  const name = options.value.find(option => option.value === id)?.label
  if (!name) return undefined
  return {
    kind: kind.value, targetId: id, name,
    node: kind.value === 'resources' ? workspace.nodes.find(node => node.id === id && !node.removedAt) : undefined,
    usage: kind.value === 'usage' ? [...workspace.usage].filter(row => row.subscriptionId === id).sort((a, b) => Date.parse(b.queriedAt) - Date.parse(a.queriedAt)) : [],
    measurements: kind.value === 'measurements' ? [...workspace.measurements].filter(row => row.routeId === id).sort((a, b) => Date.parse(b.observedAt) - Date.parse(a.observedAt)) : [],
  }
}
const current = computed(read)
const snapshot = computed(() => query.data.value ?? current.value)
const scopeMissing = computed(() => !!targetId.value && !current.value)
function clearRead() {
  task.cancel()
  query.data.value = undefined
  query.error.value = undefined
  query.queriedAt.value = undefined
  feedback.value = undefined
  now.value = Date.now()
}
watch(kind, () => { clearRead(); targetId.value = null })
watch(targetId, clearRead)
watch(resetEpoch, () => { clearRead(); targetId.value = null })
async function refresh() {
  if (!current.value || query.loading.value) return
  feedback.value = undefined
  await query.run(async () => {
    const success = await task.run('success', 500)
    if (!success) return Promise.reject(new Error('CANCELLED'))
    if (workspace.scenario === 'error' || workspace.scenario === 'offline' || current.value?.node?.control === 'offline') throw new Error('DEMO_ERROR')
    const result = read()
    if (!result) throw new Error('MISSING_SCOPE')
    return result
  })
  now.value = Date.now()
  if (task.state.value === 'cancelled') {
    query.error.value = undefined
    feedback.value = 'cancelled'
  } else if (!query.error.value) feedback.value = 'read'
}
function bytes(value?: number | null) {
  if (value == null || !Number.isFinite(value)) return t('common.unknown')
  if (value < 1024) return t('features.overview.snapshots.bytes', { value: number(value) })
  const unit = value >= 1073741824 ? 'GiB' : value >= 1048576 ? 'MiB' : 'KiB'
  const divisor = unit === 'GiB' ? 1073741824 : unit === 'MiB' ? 1048576 : 1024
  return `${new Intl.NumberFormat(locale.value, { maximumFractionDigits: 2 }).format(value / divisor)} ${unit}`
}
const resourceTime = computed(() => snapshot.value?.node?.queriedAt)
const resourceStale = computed(() => observationExpired(resourceTime.value, workspace.settings.measurementTtlHours ?? 96, now.value))
const latestMeasurement = computed(() => snapshot.value?.measurements[0])
const latestUsage = computed(() => snapshot.value?.usage[0])
function cacheExpiry(observedAt?: string) {
  if (!observedAt || !Number.isFinite(Date.parse(observedAt))) return undefined
  return new Date(Date.parse(observedAt) + (workspace.settings.measurementTtlHours ?? 96) * 3600000).toISOString()
}
</script>

<template>
  <section class="panel snapshot-panel" aria-labelledby="snapshot-title">
    <div class="section-heading"><div><h2 id="snapshot-title">{{ t('features.overview.snapshots.title') }}</h2><p>{{ t('features.overview.snapshots.hint') }}</p></div><span class="snapshot-label">{{ t('common.simulated') }}</span></div>
    <div class="scope-controls">
      <div><label for="overview-snapshot-kind">{{ t('features.overview.snapshots.kind') }}</label><NSelect :input-props="{ id: 'overview-snapshot-kind' }" v-model:value="kind" :options="kinds" filterable :disabled="query.loading.value" /></div>
      <div><label for="overview-snapshot-target">{{ t('features.overview.snapshots.target') }}</label><NSelect :input-props="{ id: 'overview-snapshot-target' }" v-model:value="targetId" :options="options" filterable clearable :placeholder="t('features.overview.snapshots.choose')" :disabled="query.loading.value" /></div>
      <NButton type="primary" :disabled="!current || query.loading.value" :loading="query.loading.value" @click="refresh">{{ t('features.overview.snapshots.refresh') }}</NButton>
      <NButton v-if="query.loading.value" @click="task.cancel">{{ t('common.cancel') }}</NButton>
    </div>
    <p class="scope-summary">{{ current ? t('features.overview.snapshots.scope', { name: current.name, kind: t(`features.overview.snapshots.${kind}`) }) : t('features.overview.snapshots.chooseHint') }}</p>
    <p v-if="!options.length" class="muted">{{ t('features.overview.snapshots.noTargets') }}</p>
    <NAlert v-if="scopeMissing" type="warning" :show-icon="false">{{ t('common.missing') }}</NAlert>
    <ErrorState v-if="query.error.value" :issue="query.error.value" :retryable="!!current && !query.loading.value" @retry="refresh" />
    <p v-if="query.error.value" class="muted">{{ t('features.overview.snapshots.failureHint') }}</p>
    <div aria-live="polite" role="status">
      <p v-if="query.loading.value">{{ t('features.overview.snapshots.loading') }}</p>
      <p v-else-if="feedback">{{ t(`features.overview.snapshots.${feedback}`) }}</p>
      <p v-if="query.queriedAt.value" class="read-time">{{ t('features.overview.snapshots.readAt', { time: date(query.queriedAt.value) }) }}</p>
    </div>
    <div v-if="snapshot && !scopeMissing" class="snapshot-result" :aria-busy="query.loading.value">
      <div class="result-heading"><strong>{{ snapshot.name }}</strong><RouterLink :to="snapshot.kind === 'resources' ? entityLocation('node', snapshot.targetId) : snapshot.kind === 'usage' ? pageLocation('subscription-detail', { subscriptionId: snapshot.targetId }, { tab: 'usage' }) : pageLocation('route-detail', { routeId: snapshot.targetId }, { tab: 'measurements' })">{{ t('features.overview.snapshots.details') }}</RouterLink></div>
      <template v-if="snapshot.node">
        <QueryStamp :queried-at="resourceTime" :scope="t('features.overview.snapshots.resources')" />
        <p v-if="resourceStale" class="stale">{{ t('features.overview.snapshots.resourceStale', { hours: workspace.settings.measurementTtlHours ?? 96 }) }}</p>
        <p v-if="snapshot.node.queryIssue" class="stale">{{ t('features.overview.snapshots.sourceFailed') }}</p>
        <dl class="metrics">
          <div><dt>{{ t('features.overview.snapshots.cpu') }}</dt><dd>{{ snapshot.node.resources?.cpuPercent == null ? t('common.unknown') : t('features.overview.snapshots.percent', { value: number(snapshot.node.resources.cpuPercent) }) }}</dd></div>
          <div><dt>{{ t('features.overview.snapshots.memory') }}</dt><dd>{{ bytes(snapshot.node.resources?.memoryBytes) }}</dd></div>
          <div><dt>{{ t('features.overview.snapshots.disk') }}</dt><dd>{{ bytes(snapshot.node.resources?.diskBytes) }}</dd></div>
          <div><dt>{{ t('features.overview.snapshots.network') }}</dt><dd>{{ snapshot.node.resources?.networkBytesPerSecond == null ? t('common.unknown') : t('features.overview.snapshots.perSecond', { value: bytes(snapshot.node.resources.networkBytesPerSecond) }) }}</dd></div>
        </dl>
        <p class="muted">{{ t('features.overview.snapshots.resourceHint') }}</p>
      </template>
      <template v-else-if="snapshot.kind === 'usage'">
        <QueryStamp :queried-at="latestUsage?.queriedAt" :expires-at="cacheExpiry(latestUsage?.queriedAt)" :scope="t('features.overview.snapshots.usage')" />
        <p v-if="!snapshot.usage.length">{{ t('features.overview.snapshots.noUsage') }}</p>
        <ul v-else class="observations"><li v-for="row in snapshot.usage.slice(0, 4)" :key="row.id">
          <strong>{{ row.name }}</strong><span>{{ t(`features.overview.snapshots.scopes.${row.scope}`) }} · {{ t(`features.overview.snapshots.metrics.${row.metric ?? 'cumulative_bytes'}`) }}</span>
          <span>{{ row.metric === 'connection_count' || row.metric === 'active_snapshot' ? number(row.connections) : row.metric === 'unavailable' ? t('common.unknown') : bytes(row.bytes) }}</span>
          <span>{{ date(row.coverageStart) }} → {{ date(row.coverageEnd) }}</span>
          <span>{{ t(row.complete ? 'features.overview.snapshots.complete' : 'features.overview.snapshots.partial') }}<template v-if="row.reset"> · {{ t('features.overview.snapshots.reset') }}</template></span>
          <QueryStamp :queried-at="row.queriedAt" :expires-at="cacheExpiry(row.queriedAt)" />
        </li></ul>
        <p>{{ t('features.overview.snapshots.usageHint') }}</p>
        <p class="muted">{{ t('features.overview.snapshots.cacheHint', { hours: workspace.settings.measurementTtlHours ?? 96 }) }}</p>
      </template>
      <template v-else>
        <QueryStamp :queried-at="latestMeasurement?.observedAt" :expires-at="latestMeasurement?.expiresAt" :scope="t('features.overview.snapshots.measurements')" />
        <p v-if="!snapshot.measurements.length">{{ t('features.overview.snapshots.noMeasurements') }}</p>
        <ul v-else class="observations"><li v-for="row in snapshot.measurements.slice(0, 4)" :key="row.id">
          <strong>{{ row.name }} · {{ t('features.overview.snapshots.revision', { value: row.revision }) }}</strong>
          <span>{{ t(`features.overview.snapshots.measurementKinds.${row.kind}`) }} · {{ display('method', row.method) }} · {{ display('scope', row.scope) }} · {{ display('environment', row.environment) }} · {{ row.transport }}</span>
          <span>{{ row.value == null ? t('common.unknown') : t('features.overview.snapshots.ms', { value: number(row.value) }) }} · {{ t('features.overview.snapshots.samples', { value: number(row.samples) }) }}</span>
          <QueryStamp :queried-at="row.observedAt" :expires-at="row.expiresAt" />
        </li></ul>
      </template>
    </div>
  </section>
</template>

<style scoped>
.section-heading, .result-heading { display: flex; gap: 16px; justify-content: space-between; align-items: baseline; flex-wrap: wrap; }
.section-heading p { margin: 0 0 20px; max-width: 720px; }
.snapshot-label { color: var(--color-primary); background: #edf5ef; padding: 4px 10px; border-radius: 6px; font-size: 12px; }
.scope-controls { display: flex; gap: 12px; flex-wrap: wrap; align-items: end; }
.scope-controls > div { flex: 1 1 180px; min-width: 0; }
.scope-controls > div:nth-child(2) { flex-grow: 2; }
.scope-controls :deep(.n-button) { min-height: 44px; }
.scope-controls :deep(.n-base-selection-label) { min-height: 44px; }
.scope-summary, .read-time, .muted { font-size: 12px; color: var(--color-muted); }
.snapshot-result { border-top: 1px solid var(--color-border); padding-top: 20px; margin-top: 16px; }
.result-heading a { display: inline-flex; min-height: 44px; align-items: center; }
.metrics { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 20px; }
.metrics dd { font-size: 18px; }
.stale { color: #8b5b17; }
.observations { list-style: none; padding: 0; margin: 12px 0; }
.observations li { display: grid; gap: 6px; padding: 14px 0; border-bottom: 1px solid var(--color-border); }
.observations li > span { font-size: 12px; color: var(--color-muted); }
.observations :deep(.query-stamp) { margin: 0; }
@media (max-width: 760px) { .metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); } .scope-controls > div { flex-basis: 100%; } }
</style>
