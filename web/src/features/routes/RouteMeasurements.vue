<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NSelect, NAlert } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { resetEpoch, useWorkspace } from '../../demo/workspace'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import { useFormat } from '../../composables/useFormat'
import QueryStamp from '../../components/QueryStamp.vue'
import TaskFeedback from '../../components/TaskFeedback.vue'
import EmptyState from '../../components/EmptyState.vue'
import EntityLink from '../../components/EntityLink.vue'
import type { Measurement } from '../../domain'
import { clone, displayedRevision, type RouteSnapshot } from './route-model'
const props = defineProps<{ route: RouteSnapshot }>()
const { workspace, commands, localClientPaired } = useWorkspace(); const { t } = useI18n(); const { date, number } = useFormat(); const { state, run, cancel } = useSimulatedTask()
const prefix = 'features.routes.'
const mode = ref('partial'); const environment = ref(workspace.settings.environment ?? 'mainland_china')
const options = computed(() => ['network_rtt', 'browser_estimate', 'partial'].map(value => ({ value, label: t(`${prefix}measurementModes.${value}`) })))
const environments = computed(() => ['mainland_china', 'outside_mainland_china'].map(value => ({ value, label: t(`${prefix}environments.${value}`) })))
const observations = computed(() => workspace.measurements.filter(item => item.routeId === props.route.id).slice().sort((a, b) => b.observedAt.localeCompare(a.observedAt)))
const latest = computed(() => observations.value[0])
watch(() => props.route.id, cancel)
watch(resetEpoch, cancel)
async function measure() {
  if (state.value === 'running') return
  const source = clone(props.route); const revision = displayedRevision(source); const selectedMode = mode.value; const selectedEnvironment = environment.value
  const offline = revision.nodeIds.some(id => { const node = workspace.nodes.find(node => node.id === id); return !node || node.control === 'offline' || !!node.removedAt })
  if (!await run(workspace.scenario === 'error' || offline ? 'error' : 'success', 650)) return
  if (props.route.id !== source.id) return
  const now = new Date().toISOString(); const expiresAt = new Date(Date.now() + (workspace.settings.measurementTtlHours ?? 96) * 3600000).toISOString()
  const batchId = crypto.randomUUID()
  const firstNode = workspace.nodes.find(node => node.id === revision.nodeIds[0])
  const entry = workspace.listeners.find(listener => listener.id === revision.entryListenerId)
  const firstTransport = entry?.protocol === 'hysteria2' ? 'udp' : 'tcp'
  const firstMeasured = selectedMode === 'network_rtt' ? localClientPaired.value && !!workspace.settings.localClient?.rawRtt : selectedMode === 'browser_estimate' && !!firstNode?.probe?.enabled
  const samples: Measurement[] = []
  const first: Measurement = {
    id: `measure-${batchId}-entry`, name: t(`${prefix}measurementEntry`), updatedAt: now, routeId: source.id,
    kind: selectedMode === 'browser_estimate' ? 'browser_http' : 'network_rtt', method: selectedMode === 'browser_estimate' ? 'simulated_browser_http' : 'simulated_local_raw_rtt',
    transport: selectedMode === 'browser_estimate' ? 'https' : firstTransport, value: firstMeasured ? 38 : null, samples: firstMeasured ? 3 : 0,
    environment: selectedEnvironment, startedAt: now, observedAt: now, expiresAt, scope: 'client-to-entry', revision: revision.revision,
    toNodeId: revision.nodeIds[0], timingBasis: selectedMode === 'browser_estimate' ? 'responseStart-requestStart' : 'raw-transport-rtt', pathVerified: false,
  }
  samples.push(first)
  revision.links.forEach((link, index) => {
    const listener = workspace.listeners.find(item => item.id === link.listenerId)
    const measured = !link.tunnelId && !!listener
    samples.push({ ...first, id: `measure-${batchId}-${index}`, name: t(`${prefix}internalHop`, { index: index + 1 }), kind: 'network_rtt', method: link.tunnelId ? 'tunnel_rtt_unavailable' : `simulated_${link.protocol === 'hysteria2' ? 'quic' : 'tcp'}_rtt`, transport: link.protocol === 'hysteria2' ? 'udp' : 'tcp', value: measured ? 16 + index * 11 : null, samples: measured ? 3 : 0, scope: `node-${link.fromNodeId}-to-${link.toNodeId}`, fromNodeId: link.fromNodeId, toNodeId: link.toNodeId, timingBasis: 'raw-transport-rtt' })
  })
  if (samples.every(sample => sample.value !== null)) {
    samples.push({ ...first, id: `measure-${batchId}-cost`, name: t(`${prefix}estimatedCost`), kind: 'path_cost', method: selectedMode === 'browser_estimate' ? 'simulated_mixed_estimate' : 'simulated_rtt_sum', scope: 'client-to-terminal', value: samples.reduce((total, sample) => total + (sample.value ?? 0), 0), transport: 'mixed' })
  }
  samples.forEach(sample => commands.upsert('measurements', sample))
}
function invalidate() {
  const now = new Date().toISOString()
  observations.value.filter(item => item.scope === 'client-to-entry').forEach(item => { const changed = clone<Measurement>(item); changed.expiresAt = now; commands.upsert('measurements', changed) })
}
</script>
<template>
  <section class="measurements">
    <NAlert type="info" :bordered="false">{{ t(`${prefix}measurementHint`) }}</NAlert>
    <div class="panel controls">
      <label>{{ t(`${prefix}measurementMode`) }}<NSelect v-model:value="mode" :options="options" :disabled="state === 'running'" :aria-label="t(`${prefix}measurementMode`)" /></label>
      <label>{{ t(`${prefix}environment`) }}<NSelect v-model:value="environment" :options="environments" :disabled="state === 'running'" :aria-label="t(`${prefix}environment`)" /></label>
      <NButton type="primary" :loading="state === 'running'" @click="measure">{{ t(`${prefix}simulateMeasure`) }}</NButton>
      <NButton :disabled="!observations.some(item => item.scope === 'client-to-entry') || state === 'running'" @click="invalidate">{{ t(`${prefix}networkChanged`) }}</NButton>
    </div>
    <p class="muted">{{ t(`${prefix}measurementPrerequisites`) }}</p>
    <TaskFeedback v-if="state !== 'idle'" :state="state" @cancel="cancel" />
    <NAlert v-if="state === 'error'" type="error">{{ t(`${prefix}measurementFailed`) }}</NAlert>
    <QueryStamp :queried-at="latest?.observedAt" :expires-at="latest?.expiresAt" :scope="t(`${prefix}routeScope`)" />
    <EmptyState v-if="!observations.length" :title="t(`${prefix}notMeasured`)" :description="t(`${prefix}measureEmpty`)" />
    <article v-for="item in observations" :key="item.id" class="panel measurement">
      <div class="measurement-heading"><strong>{{ t(`${prefix}measurementKinds.${item.kind}`) }}</strong><strong>{{ item.value === null ? t(`${prefix}notMeasured`) : `${number(item.value)} ms` }}</strong></div>
      <p>{{ item.name }} · {{ t(`${prefix}simulated`) }}<span v-if="item.revision !== displayedRevision(route).revision"> · {{ t(`${prefix}olderRevision`) }}</span></p>
      <p v-if="item.fromNodeId || item.toNodeId"><EntityLink v-if="item.fromNodeId" kind="node" :id="item.fromNodeId" :label="workspace.nodes.find(node => node.id === item.fromNodeId)?.name ?? item.fromNodeId" /> → <EntityLink v-if="item.toNodeId" kind="node" :id="item.toNodeId" :label="workspace.nodes.find(node => node.id === item.toNodeId)?.name ?? item.toNodeId" /></p>
      <dl><div><dt>{{ t(`${prefix}method`) }}</dt><dd class="mono">{{ item.method }} · {{ item.transport }}</dd></div><div><dt>{{ t(`${prefix}scope`) }}</dt><dd>{{ item.scope }} · r{{ item.revision }}</dd></div><div><dt>{{ t(`${prefix}environment`) }}</dt><dd>{{ item.environment }}</dd></div><div><dt>{{ t(`${prefix}samples`) }}</dt><dd>{{ item.samples }} · {{ t(`${prefix}failedSamples`, { count: item.failedSamples ?? t(`${prefix}unknown`) }) }} · {{ t(`${prefix}dispersion`, { value: number(item.dispersion) }) }}</dd></div><div><dt>{{ t(`${prefix}timing`) }}</dt><dd>{{ item.timingBasis ?? t(`${prefix}unknown`) }} · {{ t(`${prefix}${item.pathVerified ? 'pathVerified' : 'pathUnverified'}`) }}</dd></div><div><dt>{{ t(`${prefix}expires`) }}</dt><dd>{{ date(item.expiresAt) }}</dd></div></dl>
      <QueryStamp :queried-at="item.observedAt" :expires-at="item.expiresAt" :scope="item.scope" />
      <p v-if="item.kind === 'browser_http'" class="muted">{{ t(`${prefix}httpWarning`) }}</p><p v-if="item.kind === 'path_cost'" class="muted">{{ t(`${prefix}estimateWarning`) }}</p>
    </article>
  </section>
</template>
<style scoped>
.measurements { display: grid; gap: 16px; }.controls { display: flex; gap: 12px; flex-wrap: wrap; align-items: end; }.controls label { display: grid; gap: 6px; min-width: 220px; flex: 1; }.measurement-heading { display: flex; justify-content: space-between; gap: 12px; }.measurement dl { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }.measurement dt { color: #63736a; font-size: 12px; }.measurement dd { margin: 4px 0 0; overflow-wrap: anywhere; }.mono { font-family: ui-monospace, monospace; }.muted { color: #63736a; font-size: 13px; }@media (max-width: 650px) { .measurement dl { grid-template-columns: 1fr; }.controls label { min-width: 100%; } }
</style>
