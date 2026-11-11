<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NButton, NInput, NInputNumber, NSwitch, NSelect, NModal, NAlert, useMessage } from 'naive-ui'
import { pageLocation, entityLocation } from '../../../app/router'
import type { ProtocolSettings, Listener, Issue } from '../../../domain'
import ConfirmImpactDialog from '../../../components/ConfirmImpactDialog.vue'
import ValidationSummary from '../../../components/ValidationSummary.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import TaskFeedback from '../../../components/TaskFeedback.vue'
import { useNodeContext, useNodeRelease } from '../useNodeContext'
import { cloneNode, cloneListener, defaultsFor, issue, validIp, validPort, portConflict, validateDefaults } from '../model'
import { useEditorGuard } from '../useEditorGuard'
const props = defineProps<{ nodeId: string }>()
const { node, workspace, commands, listeners, routes } = useNodeContext(props.nodeId)
const { t } = useI18n(); const message = useMessage(); const key = 'features.node-detail.'
const rows = ref<ProtocolSettings[]>(defaultsFor(node.value))
const baseline = ref(JSON.stringify(rows.value))
const dirty = computed(() => JSON.stringify(rows.value) !== baseline.value)
const issues = ref<Issue[]>([])
const confirmDefaults = ref(false)
const listenerSearch = ref('')
const selectedId = ref<string>()
const listenerForm = ref<Listener>()
const listenerBaseline = ref('')
const listenerDirty = computed(() => !!listenerForm.value && JSON.stringify(listenerForm.value) !== listenerBaseline.value)
const releaseTask = useNodeRelease(props.nodeId)
const lastReleaseId = ref<string>()
const selectedListener = computed(() => listeners.value.find(l => l.id === selectedId.value))
const listenerRoutes = computed(() => workspace.routes.filter(r => [r.draft, r.published].some(rev => rev && (rev.entryListenerId === selectedId.value || rev.links.some(link => link.listenerId === selectedId.value))) || !!selectedListener.value?.routeIds.includes(r.id)))
const listenerEntries = computed(() => workspace.entries.filter(e => e.listenerId === selectedId.value))
const listenerTunnels = computed(() => workspace.tunnels.filter(t => t.targetListenerId === selectedId.value))
const referenced = computed(() => listenerRoutes.value.length + listenerEntries.value.length + listenerTunnels.value.length > 0)
const filtered = computed(() => listeners.value.filter(l => `${l.name} ${l.port} ${l.address} ${l.protocol}`.toLowerCase().includes(listenerSearch.value.trim().toLowerCase())))
const impacts = computed(() => [t(key + 'defaultsOnly'), t(key + 'referencedRoutes', { names: routes.value.map(r => r.name).join(', ') }), t(key + 'listenersKept', { count: listeners.value.length })])
const transports = computed(() => ['tcp', 'udp', 'tcp_udp'].map(value => ({ value, label: t(key + 'transports.' + value) })))
const ciphers = ['2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm'].map(value => ({ value, label: value }))
function discard() { rows.value = defaultsFor(node.value); baseline.value = JSON.stringify(rows.value); listenerForm.value = undefined; selectedId.value = undefined; issues.value = [] }
useEditorGuard(computed(() => dirty.value || listenerDirty.value), discard)
watch(() => node.value.protocolDefaults, () => { if (!dirty.value) { rows.value = defaultsFor(node.value); baseline.value = JSON.stringify(rows.value) } })
function focus(field: string) { const el = document.getElementById(field); (el?.querySelector('input') ?? el)?.focus() }
function requestSave() {
  issues.value = validateDefaults(node.value, rows.value, workspace.listeners)
  if (issues.value.length) { focus(issues.value[0]?.field ?? ''); return }
  if (routes.value.length) confirmDefaults.value = true
  else saveDefaults()
}
function saveDefaults() {
  issues.value = validateDefaults(node.value, rows.value, workspace.listeners)
  if (issues.value.length || node.value.removedAt) { confirmDefaults.value = false; return }
  const next = cloneNode(node.value)
  next.protocolDefaults = JSON.parse(JSON.stringify(rows.value)); next.updatedAt = new Date().toISOString()
  commands.upsert('nodes', next)
  baseline.value = JSON.stringify(rows.value); confirmDefaults.value = false
  message.success(t(key + 'defaultsSaved'))
}
function inspect(listener: typeof listeners.value[number]) {
  selectedId.value = listener.id; listenerForm.value = cloneListener(listener); listenerBaseline.value = JSON.stringify(listenerForm.value); issues.value = []
}
function closeListener() { releaseTask.cancel(); listenerForm.value = undefined; selectedId.value = undefined; issues.value = [] }
function validateListener() {
  const form = listenerForm.value
  const result: Issue[] = []
  if (!form) return result
  if (!validIp(form.address)) result.push(issue('LISTEN_ADDRESS', 'actual-address'))
  if (!validPort(form.port)) result.push(issue('PORT', 'actual-port'))
  const conflict = portConflict(workspace.listeners, props.nodeId, form.address, form.port, form.transport, form.id)
  if (conflict) result.push(issue('PORT_IN_USE', 'actual-port', { port: form.port, name: conflict.name }))
  if (node.value.mode === 'reverse' && (form.address !== '127.0.0.1' || form.transport !== 'tcp')) result.push(issue('REVERSE_LOCK'))
  return result
}
async function saveListener() {
  if (!listenerForm.value || referenced.value || node.value.removedAt || releaseTask.state.value === 'running') return
  issues.value = validateListener()
  if (issues.value.length) return
  const edited = cloneListener(listenerForm.value)
  const result = await releaseTask.publish(t(key + 'listenerRelease', { name: edited.name }), edited.revision + 1, () => {
    // Recheck dependencies after the finite task: another page may have allocated this resource.
    if (referenced.value || validateListener().length) return false
    edited.revision++; edited.updatedAt = new Date().toISOString(); edited.state = 'published'
    commands.upsert('listeners', edited)
    const next = cloneNode(node.value); next.appliedRevision = (next.appliedRevision ?? 0) + 1; next.updatedAt = edited.updatedAt; commands.upsert('nodes', next)
    for (const original of workspace.measurements.filter(m => routes.value.some(r => r.id === m.routeId))) commands.upsert('measurements', { ...original, expiresAt: edited.updatedAt })
    listenerForm.value = cloneListener(edited); listenerBaseline.value = JSON.stringify(listenerForm.value)
  }, [t(key + 'validateListener'), t(key + 'applyListener'), t(key + 'verifyListener')])
  if (result) lastReleaseId.value = result.id
}
function referenceCount(listener: typeof listeners.value[number]) {
  return new Set([...listener.routeIds, ...workspace.routes.filter(r => [r.draft, r.published].some(rev => rev && (rev.entryListenerId === listener.id || rev.links.some(link => link.listenerId === listener.id)))).map(r => r.id)]).size + workspace.entries.filter(e => e.listenerId === listener.id).length + workspace.tunnels.filter(t => t.targetListenerId === listener.id).length
}
function nextHop(listener: typeof listeners.value[number]) { return workspace.listeners.find(l => l.id === listener.nextHopListenerId)?.name ?? (listener.nextHopListenerId || t(key + 'terminalOrRevision')) }
</script>
<template>
  <div>
    <section class="panel">
      <h2>{{ t(key + 'protocolDefaults') }}</h2><p>{{ t(key + 'defaultsOnly') }}</p>
      <NAlert v-if="node.mode === 'reverse'" type="info">{{ t(key + 'reverseLock') }}</NAlert>
      <ValidationSummary :issues="selectedId ? [] : issues" @focus="focus" />
      <form @submit.prevent="requestSave">
        <fieldset v-for="row in rows" :id="`protocol-${row.protocol}`" :key="row.protocol" :disabled="!!node.removedAt" tabindex="-1">
          <legend>{{ t(key + 'protocolNames.' + row.protocol) }}</legend>
          <div class="protocol-grid">
            <label>{{ t(key + 'enabled') }}<NSwitch v-model:value="row.enabled" :aria-label="t(key + 'enabledFor', { protocol: t(key + 'protocolNames.' + row.protocol) })" /></label>
            <label>{{ t(key + 'listenAddress') }}<NInput v-model:value="row.listenAddress" :disabled="node.mode === 'reverse'" :input-props="{ 'aria-label': t(key + 'listenAddress') }" /></label>
            <label>{{ t(key + 'automaticPort') }}<NSwitch v-model:value="row.autoPort" :aria-label="t(key + 'automaticPort')" /></label>
            <label>{{ t(key + 'preferredPort') }}<NInputNumber :value="row.preferredPort" :min="1" :max="65535" :disabled="row.autoPort" :aria-label="t(key + 'preferredPort')" @update:value="value => row.preferredPort = value ?? 0" /></label>
          </div>
          <p>{{ t(key + 'transports.' + (row.transport ?? 'tcp')) }} · {{ t(key + 'businessTcp') }}: {{ t('status.' + node.capabilities.tcp) }} · {{ t(key + 'businessUdp') }}: {{ node.mode === 'reverse' ? t(key + 'rejected') : t('status.' + node.capabilities.udp) }}</p>
          <details><summary>{{ t(key + 'advanced') }}</summary>
            <p>{{ t(key + 'defaultPool', { start: workspace.settings.defaultPortStart, end: workspace.settings.defaultPortEnd }) }}</p>
            <div v-for="(range, index) in row.portPool" :key="index" class="pool-row"><label>{{ t(key + 'poolStart') }}<NInputNumber :value="range.start" :min="1" :max="65535" @update:value="value => range.start = value ?? 0" /></label><label>{{ t(key + 'poolEnd') }}<NInputNumber :value="range.end" :min="1" :max="65535" @update:value="value => range.end = value ?? 0" /></label><NButton @click="row.portPool?.splice(index, 1)">{{ t(key + 'removeRange') }}</NButton></div>
            <NButton @click="row.portPool?.push({ start: workspace.settings.defaultPortStart, end: workspace.settings.defaultPortEnd })">{{ t(key + 'addRange') }}</NButton>
            <div class="advanced-grid">
              <label v-if="row.protocol === 'ss2022'">{{ t(key + 'cipher') }}<NSelect v-model:value="row.cipher" :options="ciphers" /></label>
              <label v-if="row.protocol !== 'ss2022'">{{ t(key + 'serverName') }}<NInput v-model:value="row.serverName" :maxlength="253" /></label>
              <template v-if="row.protocol === 'vless'"><label>{{ t(key + 'publicKey') }}<NInput v-model:value="row.publicKey" :maxlength="64" /></label><label>{{ t(key + 'shortId') }}<NInput v-model:value="row.shortId" :maxlength="16" /></label><label>{{ t(key + 'flow') }}<NSelect v-model:value="row.flow" :options="[{ value: 'xtls-rprx-vision', label: 'xtls-rprx-vision' }, { value: '', label: t(key + 'none') }]" /></label></template>
            </div>
            <p v-if="row.protocol === 'trojan' || row.protocol === 'hysteria2'">{{ t(key + 'certificateHint') }} <RouterLink :to="pageLocation('settings', {}, { section: 'certificates' })">{{ t(key + 'certificates') }}</RouterLink></p>
            <p v-if="row.protocol === 'hysteria2'">{{ t(key + 'hy2Hint') }}</p>
          </details>
        </fieldset>
        <div class="actions"><NButton type="primary" attr-type="submit" :disabled="!dirty || !!node.removedAt">{{ t(key + 'saveDefaults') }}</NButton><NButton :disabled="!dirty" @click="discard">{{ t('common.discard') }}</NButton><span v-if="dirty" role="status">{{ t(key + 'unsaved') }}</span></div>
      </form>
    </section>
    <section class="panel">
      <h2>{{ t(key + 'actualListeners') }}</h2><p>{{ t(key + 'listenerHint') }}</p>
      <NInput v-model:value="listenerSearch" clearable :placeholder="t(key + 'searchListeners')" :input-props="{ 'aria-label': t(key + 'searchListeners') }" />
      <div v-if="filtered.length" class="table-scroll"><table><thead><tr><th>{{ t(key + 'name') }}</th><th>{{ t(key + 'actualEndpoint') }}</th><th>{{ t(key + 'transport') }}</th><th>{{ t(key + 'nextHop') }}</th><th>{{ t(key + 'references') }}</th><th>{{ t(key + 'publishState') }}</th><th>{{ t(key + 'actions') }}</th></tr></thead><tbody>
        <tr v-for="listener in filtered" :key="listener.id"><td>{{ listener.name }}<small>{{ t(key + 'protocolNames.' + listener.protocol) }} · r{{ listener.revision }}</small></td><td><code>{{ listener.address }}:{{ listener.port }}</code></td><td>{{ t(key + 'transports.' + listener.transport) }}</td><td>{{ nextHop(listener) }}</td><td>{{ referenceCount(listener) }}</td><td><StatusBadge :status="listener.state ?? 'unknown'" :label="listener.state ? t(key + 'releaseStates.' + listener.state) : t('common.unknown')" /><small v-if="listener.cleanup === 'pending'">{{ t(key + 'pendingCleanup') }}</small></td><td><NButton @click="inspect(listener)">{{ t(key + 'inspectListener') }}</NButton></td></tr>
      </tbody></table></div>
      <p v-else role="status">{{ t(key + (listeners.length ? 'noMatchingListeners' : 'noListeners')) }}</p>
      <NButton v-if="!filtered.length && listeners.length" @click="listenerSearch = ''">{{ t('common.clear') }}</NButton>
    </section>
    <ConfirmImpactDialog v-model:show="confirmDefaults" :title="t(key + 'defaultsImpact')" :impacts="impacts" @confirm="saveDefaults" />
    <NModal :show="!!selectedId" preset="card" :title="t(key + 'inspectListener')" style="width: min(720px, 94vw)" @update:show="value => { if (!value) closeListener() }">
      <template v-if="listenerForm">
        <p>{{ listenerForm.name }} · {{ t(key + 'references') }}: {{ referenceCount(listenerForm) }}</p>
        <NAlert v-if="referenced" type="warning">{{ t(key + 'sharedListenerLocked') }}</NAlert>
        <ul><li v-for="r in listenerRoutes" :key="r.id"><RouterLink :to="pageLocation('route-edit', { routeId: r.id }, { step: 'review' })">{{ r.name }}</RouterLink></li><li v-for="entry in listenerEntries" :key="entry.id"><RouterLink :to="entityLocation('subscription', entry.subscriptionId)">{{ entry.name }}</RouterLink></li><li v-for="tunnel in listenerTunnels" :key="tunnel.id">{{ tunnel.name }} · {{ t(key + 'mappingPort') }} {{ tunnel.mappingPort }}</li></ul>
        <ValidationSummary :issues="issues" @focus="focus" />
        <fieldset :disabled="referenced || !!node.removedAt || releaseTask.state.value === 'running'"><legend>{{ t(key + 'unreferencedEditor') }}</legend><label>{{ t(key + 'listenAddress') }}<NInput v-model:value="listenerForm.address" :disabled="node.mode === 'reverse'" :input-props="{ id: 'actual-address' }" /></label><label>{{ t(key + 'actualPort') }}<NInputNumber :value="listenerForm.port" :min="1" :max="65535" :input-props="{ id: 'actual-port' }" @update:value="value => { if (listenerForm) listenerForm.port = value ?? 0 }" /></label><label>{{ t(key + 'transport') }}<NSelect v-model:value="listenerForm.transport" :options="transports.filter(option => node.mode === 'reverse' ? option.value === 'tcp' : listenerForm?.protocol === 'hysteria2' ? option.value === 'udp' : listenerForm?.protocol === 'ss2022' ? true : option.value === 'tcp')" /></label></fieldset>
        <TaskFeedback v-if="releaseTask.state.value !== 'idle'" :state="releaseTask.state.value" @cancel="releaseTask.cancel" />
        <div class="actions"><NButton type="primary" :disabled="referenced || !listenerDirty || !!node.removedAt" :loading="releaseTask.state.value === 'running'" @click="saveListener">{{ t(key + 'simulateListenerSave') }}</NButton><NButton @click="closeListener">{{ t('common.cancel') }}</NButton><RouterLink v-if="lastReleaseId" :to="entityLocation('release', lastReleaseId)">{{ t(key + 'viewRelease') }}</RouterLink></div>
      </template>
    </NModal>
  </div>
</template>
<style scoped>
fieldset { border: 1px solid var(--color-border); border-radius: 8px; margin: 24px 0; padding: 20px; min-width: 0; }legend { padding: 0 8px; font-weight: 600; }.protocol-grid { display: grid; grid-template-columns: 100px minmax(140px, 1fr) 120px 150px; gap: 16px; align-items: start; }.protocol-grid label { display: grid; gap: 10px; }.advanced-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; margin-top: 20px; }.pool-row { display: flex; align-items: center; gap: 12px; }.pool-row label { flex: 1; min-width: 0; }table { border-collapse: collapse; width: 100%; min-width: 850px; margin-top: 16px; }th,td { text-align: left; padding: 16px 12px; border-bottom: 1px solid var(--color-border); }small { display: block; color: var(--color-muted); margin-top: 8px; }@media(max-width:1000px){.protocol-grid{grid-template-columns:repeat(2,minmax(0,1fr))}}@media(max-width:600px){.protocol-grid,.advanced-grid{grid-template-columns:1fr}.pool-row{flex-wrap:wrap}}
</style>
