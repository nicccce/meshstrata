<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NInput, NSwitch, NAlert, useMessage } from 'naive-ui'
import type { Issue } from '../../../domain'
import { entityLocation, pageLocation } from '../../../app/router'
import { useFormat } from '../../../composables/useFormat'
import ValidationSummary from '../../../components/ValidationSummary.vue'
import TaskFeedback from '../../../components/TaskFeedback.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import { cloneNode, safeHttps, issue, validPort } from '../model'
import { useNodeContext, useNodeTask, useNodeRelease } from '../useNodeContext'
import { useEditorGuard } from '../useEditorGuard'
const props = defineProps<{ nodeId: string }>()
const { node, tunnels, workspace, commands } = useNodeContext(props.nodeId)
const { t } = useI18n(); const { date } = useFormat(); const message = useMessage(); const key = 'features.node-detail.'
function current() { const host = node.value.address ?? 'node.example.invalid'; return { enabled: node.value.probe?.enabled ?? false, url: node.value.probe?.url ?? `https://${host.includes(':') ? '[' + host + ']' : host}/meshstrata/probe` } }
const form = ref(current()); const baseline = ref(JSON.stringify(form.value))
const dirty = computed(() => JSON.stringify(form.value) !== baseline.value)
const issues = ref<Issue[]>([]); const releaseId = ref<string>(); const checkAt = ref<string>(); const checkResult = ref<'success' | 'error'>()
const publishTask = useNodeRelease(props.nodeId); const checkTask = useNodeTask()
const relay = computed(() => workspace.nodes.find(n => n.id === workspace.settings.relayNodeId))
const reverseReady = computed(() => !!relay.value && relay.value.role === 'both' && !relay.value.removedAt && (workspace.settings.relay?.enabled ?? true))
const tunnelRows = computed(() => tunnels.value.map(tunnel => ({ tunnel, remote: workspace.nodes.find(n => n.id === tunnel.remoteNodeId), relay: workspace.nodes.find(n => n.id === tunnel.relayNodeId), listener: workspace.listeners.find(l => l.id === tunnel.targetListenerId) })))
const reverseIssues = computed(() => {
  const result: Issue[] = []
  if (!reverseReady.value) result.push(issue('RELAY_REQUIRED'))
  if (!tunnelRows.value.length) result.push(issue('TUNNEL_MISSING'))
  for (const row of tunnelRows.value) {
    if (row.tunnel.relayNodeId !== workspace.settings.relayNodeId || row.relay?.role !== 'both') result.push(issue('FIXED_RELAY'))
    if (!row.listener || !['127.0.0.1', '::1'].includes(row.listener.address) || row.listener.protocol !== 'ss2022' || row.listener.transport !== 'tcp' || row.listener.nodeId !== row.tunnel.remoteNodeId) result.push(issue('REVERSE_LOCK'))
    if (!validPort(row.tunnel.mappingPort)) result.push(issue('MAPPING_PORT'))
    if (workspace.tunnels.some(other => other.id !== row.tunnel.id && other.relayNodeId === row.tunnel.relayNodeId && other.mappingPort === row.tunnel.mappingPort)) result.push(issue('MAPPING_CONFLICT'))
  }
  return result
})
function discard() { form.value = current(); baseline.value = JSON.stringify(form.value); issues.value = [] }
useEditorGuard(dirty, discard)
watch(() => node.value.probe, () => { if (!dirty.value) discard() })
function validate() { issues.value = form.value.enabled && !safeHttps(form.value.url.trim()) ? [issue('HTTPS', 'probe-url')] : []; return !issues.value.length }
async function save() {
  if (!validate() || publishTask.state.value === 'running' || node.value.removedAt || !dirty.value) return
  const value = { ...form.value, url: form.value.url.trim() }
  // Even a disabled endpoint must not persist credentials or other sensitive URL fields.
  if (value.url && !safeHttps(value.url)) { issues.value = [issue('HTTPS', 'probe-url')]; return }
  const oldRevision = node.value.probe?.revision ?? 0
  const result = await publishTask.publish(t(key + 'probeRelease', { name: node.value.name }), oldRevision + 1, () => {
    if ((node.value.probe?.revision ?? 0) !== oldRevision) return false
    const next = cloneNode(node.value); next.probe = { ...value, revision: oldRevision + 1, status: 'not_checked' }; next.appliedRevision = (next.appliedRevision ?? 0) + 1; next.updatedAt = new Date().toISOString()
    commands.upsert('nodes', next)
    for (const original of workspace.measurements.filter(m => ['browser_http', 'path_cost'].includes(m.kind) && (m.fromNodeId === props.nodeId || workspace.routes.some(r => r.id === m.routeId && (r.published ?? r.draft).nodeIds[0] === props.nodeId)))) commands.upsert('measurements', { ...original, expiresAt: next.updatedAt })
    form.value = current(); baseline.value = JSON.stringify(form.value); checkAt.value = undefined; checkResult.value = undefined
  }, [t(key + 'validateProbe'), t(key + 'applyProbe'), t(key + 'verifyProbeConfig')])
  if (result) { releaseId.value = result.id; if (result.success) message.success(t(key + 'probeSaved')) }
}
async function simulateCheck() {
  if (checkTask.state.value === 'running' || node.value.removedAt) return
  const revision = node.value.probe?.revision ?? 0
  const invalid = workspace.scenario === 'error' || node.value.control !== 'online' || (node.value.mode === 'reverse' && (reverseIssues.value.length > 0 || node.value.tunnel !== 'online'))
  const success = await checkTask.run(invalid ? 'error' : 'success')
  if (checkTask.state.value === 'cancelled') return
  checkAt.value = new Date().toISOString(); checkResult.value = success ? 'success' : 'error'
  if (node.value.mode === 'direct') {
    if (revision !== (node.value.probe?.revision ?? 0) || !node.value.probe) { checkAt.value = undefined; checkResult.value = undefined; return }
    const next = cloneNode(node.value); if (next.probe) next.probe.status = success ? 'verified' : 'failed'; commands.upsert('nodes', next)
  }
  commands.upsert('events', { id: `connectivity-${props.nodeId}-${Date.now()}`, name: t(key + (node.value.mode === 'reverse' ? 'reverseCheckEvent' : 'probeCheckEvent')), entityType: 'node', entityId: props.nodeId, kind: 'manual_check', status: success ? 'simulated_success' : 'failed', updatedAt: checkAt.value })
}
function focus(field: string) { document.getElementById(field)?.focus() }
</script>
<template>
  <section class="panel">
    <template v-if="node.mode === 'direct'">
      <h2>{{ t(key + 'httpsProbe') }}</h2><p>{{ t(key + 'probeHint') }}</p>
      <NAlert type="info">{{ t(key + 'probeBoundary') }}</NAlert>
      <ValidationSummary :issues="issues" @focus="focus" />
      <form @submit.prevent="save"><fieldset :disabled="!!node.removedAt || publishTask.state.value === 'running'"><label class="switch-label">{{ t(key + 'probeEnabled') }}<NSwitch v-model:value="form.enabled" :aria-label="t(key + 'probeEnabled')" /></label><label for="probe-url">{{ t(key + 'probeUrl') }}</label><NInput v-model:value="form.url" :input-props="{ id: 'probe-url' }" :maxlength="512" /><p>{{ t(key + 'httpsOnly') }}</p></fieldset><div class="actions"><NButton type="primary" attr-type="submit" :disabled="!dirty || !!node.removedAt || checkTask.state.value === 'running'" :loading="publishTask.state.value === 'running'">{{ t(key + 'saveProbe') }}</NButton><NButton :disabled="!dirty || publishTask.state.value === 'running'" @click="discard">{{ t('common.discard') }}</NButton></div></form>
      <dl class="details"><div><dt>{{ t(key + 'endpointRevision') }}</dt><dd>{{ node.probe?.revision ?? t(key + 'notConfigured') }}</dd></div><div><dt>{{ t(key + 'endpointStatus') }}</dt><dd><StatusBadge :status="node.probe?.status ?? 'not_checked'" /></dd></div></dl>
      <NButton :disabled="!node.probe?.enabled || dirty || !!node.removedAt || publishTask.state.value === 'running'" :loading="checkTask.state.value === 'running'" @click="simulateCheck">{{ t(key + 'simulateProbe') }}</NButton>
      <p v-if="!node.probe?.enabled">{{ t(key + 'enableProbeFirst') }}</p>
    </template>
    <template v-else>
      <h2>{{ t(key + 'reverseConnectivity') }}</h2><p>{{ t(key + 'reverseDirection') }}</p>
      <NAlert v-if="!reverseReady" type="warning">{{ t(key + 'relayPrerequisite') }} <RouterLink :to="pageLocation('settings', {}, { section: 'relay' })">{{ t(key + 'configureRelay') }}</RouterLink></NAlert>
      <dl class="details"><div><dt>{{ t(key + 'fixedRelay') }}</dt><dd><RouterLink v-if="relay" :to="entityLocation('node', relay.id)">{{ relay.name }}</RouterLink><span v-else>{{ t('common.notFound') }}</span></dd></div><div><dt>{{ t(key + 'tunnelTransport') }}</dt><dd>{{ t(key + 'tcpMtls') }}</dd></div></dl>
      <ValidationSummary :issues="reverseIssues" />
      <article v-for="row in tunnelRows" :key="row.tunnel.id" class="tunnel-card">
        <h3>{{ row.tunnel.name }} · r{{ row.tunnel.revision }}</h3>
        <dl class="details"><div><dt>{{ t(key + 'relayEndpoint') }}</dt><dd>{{ row.tunnel.relayEndpoint }}</dd></div><div><dt>{{ t(key + 'remoteListener') }}</dt><dd>{{ row.listener ? `${row.listener.address}:${row.listener.port}` : t('common.notFound') }}<small>{{ row.listener?.name ?? row.tunnel.targetListenerId }}</small></dd></div><div><dt>{{ t(key + 'relayMapping') }}</dt><dd>127.0.0.1:{{ row.tunnel.mappingPort }}</dd></div><div><dt>{{ t(key + 'appliedEnds') }}</dt><dd>{{ t(key + 'relay') }}: {{ t(key + (row.tunnel.relayApplied ? 'applied' : 'notApplied')) }}<br>{{ t(key + 'remote') }}: {{ t(key + (row.tunnel.remoteApplied ? 'applied' : 'notApplied')) }}</dd></div><div><dt>{{ t(key + 'tunnelRtt') }}</dt><dd>{{ t(key + 'notMeasured') }}</dd></div><div><dt>{{ t(key + 'updatedAt') }}</dt><dd>{{ date(row.tunnel.updatedAt) }}</dd></div></dl>
        <p>{{ t(key + 'reverseLock') }}</p><div class="links"><RouterLink v-for="routeId in row.tunnel.routeIds" :key="routeId" :to="entityLocation('route', routeId)">{{ workspace.routes.find(route => route.id === routeId)?.name ?? routeId }}</RouterLink></div>
      </article>
      <p v-if="!tunnelRows.length">{{ t(key + 'noTunnel') }} <RouterLink :to="pageLocation('route-new', {}, { exitNodeId: nodeId, step: 'exit' })">{{ t(key + 'createExit') }}</RouterLink></p>
      <NButton :disabled="!!node.removedAt" :loading="checkTask.state.value === 'running'" @click="simulateCheck">{{ t(key + 'simulateReverse') }}</NButton>
      <p>{{ t(key + 'reverseCheckBoundary') }}</p>
    </template>
    <TaskFeedback v-if="publishTask.state.value !== 'idle'" :state="publishTask.state.value" @cancel="publishTask.cancel" />
    <TaskFeedback v-if="checkTask.state.value === 'running' || checkTask.state.value === 'cancelled'" :state="checkTask.state.value" @cancel="checkTask.cancel" />
    <NAlert v-if="checkResult" class="result" :type="checkResult === 'success' ? 'success' : 'error'" :title="t(key + (checkResult === 'success' ? 'simulatedCheckSuccess' : 'simulatedCheckFailure'))">{{ t(key + 'checkResultHint') }} {{ date(checkAt) }}</NAlert>
    <div v-if="releaseId" class="actions"><RouterLink :to="entityLocation('release', releaseId)">{{ t(key + 'viewRelease') }}</RouterLink></div>
  </section>
</template>
<style scoped>
fieldset { border: 0; padding: 0; margin: 20px 0; min-width: 0; }label { margin-top: 20px; }.switch-label { display: flex; gap: 16px; align-items: center; }.details { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 24px; margin: 24px 0; }.details > div { min-width: 0; }.tunnel-card { padding: 20px; border: 1px solid var(--color-border); border-radius: 8px; margin: 24px 0; }.links { display: flex; gap: 16px; flex-wrap: wrap; }small { display:block; margin-top:8px; color:var(--color-muted); }.result { margin-top:24px; }@media(max-width:760px){.details{grid-template-columns:1fr}}
</style>
