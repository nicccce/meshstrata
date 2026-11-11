<script setup lang="ts">
import { networkRegulationLevels } from '../../domain/access-environment'
import { computed, h, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NCheckbox, NInput, NInputNumber, NModal, NSelect, NTag, useDialog, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import PageHeader from '../../components/PageHeader.vue'
import ControllerInfo from '../../components/ControllerInfo.vue'
import QueryStamp from '../../components/QueryStamp.vue'
import { entityLocation, pageLocation } from '../../app/router'
import { resetEpoch, useWorkspace } from '../../demo/workspace'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import { useFormat } from '../../composables/useFormat'
import type { Settings } from '../../domain'
import Field from './SettingField.vue'
import { initialForm, sectionPatch, sections, validate, type Errors, type Section } from './model'

const { t } = useI18n()
const text = (key: string) => t(`features.settings.${key}`)
const { workspace, commands, sessionSecrets, localClientPaired: clientPaired } = useWorkspace()
const route = useRoute(), router = useRouter(), dialog = useDialog(), message = useMessage()
const { date } = useFormat()
const task = useSimulatedTask()
const section = computed<Section>(() => sections.find(value => value === route.query.section) ?? 'general')
function freshForm() {
  const settings: Settings = JSON.parse(JSON.stringify(workspace.settings))
  const value = initialForm(settings)
  if (!value.localClient.allowedOrigin) value.localClient.allowedOrigin = window.location.origin
  // Old F00 snapshots have no mapping pool. Retain their existing allocations.
  if (!settings.relay) {
    const ports = workspace.tunnels.filter(item => item.relayNodeId === settings.relayNodeId).map(item => item.mappingPort)
    value.relay.mappingPool = { start: Math.min(30000, ...ports), end: Math.max(30999, ...ports) }
  }
  return value
}
const form = reactive(freshForm()), baseline = ref(freshForm())
const errors = ref<Errors>({}), feedback = ref(''), failed = ref(false), rotationOpen = ref(false)
const showPrivateKey = ref(false)
const locationQueriedAt = ref<string>(), certificateReviewedAt = ref<string>()
const tempNames = ['locationKey', 'dnsCredential', 'certificatePem', 'privateKeyPem', 'clientSecret', 'currentPassword', 'newPassword', 'confirmPassword'] as const
type SecretName = typeof tempNames[number]
function memory(name: SecretName) { return computed({ get: () => sessionSecrets.get(`settings:input:${name}`) ?? '', set: (value: string) => { if (value) sessionSecrets.set(`settings:input:${name}`, value); else sessionSecrets.delete(`settings:input:${name}`) } }) }
const locationKey = memory('locationKey'), dnsCredential = memory('dnsCredential'), certificatePem = memory('certificatePem'), privateKeyPem = memory('privateKeyPem'), clientSecret = memory('clientSecret')
const currentPassword = memory('currentPassword'), newPassword = memory('newPassword'), confirmPassword = memory('confirmPassword')
function namesFor(group: Section): readonly SecretName[] {
  return group === 'location' ? ['locationKey'] : group === 'certificates' ? ['dnsCredential', 'certificatePem', 'privateKeyPem'] : group === 'client' ? ['clientSecret'] : group === 'admin' ? ['currentPassword', 'newPassword', 'confirmPassword'] : []
}
function clearInputs(group?: Section) { for (const name of group ? namesFor(group) : tempNames) sessionSecrets.delete(`settings:input:${name}`) }
function isDirty(group: Section) { return JSON.stringify(sectionPatch(group, form)) !== JSON.stringify(sectionPatch(group, baseline.value)) || namesFor(group).some(name => sessionSecrets.has(`settings:input:${name}`)) }
const dirty = computed(() => isDirty(section.value))
const relayNode = computed(() => workspace.nodes.find(node => node.id === workspace.settings.relayNodeId && !node.removedAt))
const relayEligible = computed(() => relayNode.value?.role === 'both' && relayNode.value.mode === 'direct')
const tunnels = computed(() => workspace.tunnels.filter(item => item.relayNodeId === workspace.settings.relayNodeId))
const affectedRoutes = computed(() => workspace.routes.filter(item => tunnels.value.some(tunnel => tunnel.routeIds.includes(item.id))))

const options = computed(() => sections.map(value => ({ value, label: text(`sections.${value}`) })))
const environments = computed(() => networkRegulationLevels.map(value => ({ value, label: text(value === 'strong_regulation' ? 'strong' : 'weak') })))
const levels = computed(() => ['error', 'warn', 'info', 'debug'].map(value => ({ value, label: text(`${value}Level`) })))
const providers = computed(() => [{ value: 'demo', label: text('demoProvider') }, { value: 'custom', label: text('customProvider') }])
function changeLocale(value: string) {
  form.locale = value; baseline.value.locale = value
  commands.updateSettings({ locale: value })
}
const portFields = ['defaultPortStart', 'defaultPortEnd', 'webPort', 'controlPort'] as const
const logFields = [{ key: 'fileMiB', max: 1024 }, { key: 'fileCount', max: 100 }, { key: 'totalMiB', max: 10240 }, { key: 'retentionDays', max: 365 }] as const
const error = (field: string) => errors.value[field] ? text(`errors.${errors.value[field]}`) : undefined
function inputProps(id: string) { return { id, 'aria-labelledby': `${id}-label`, 'aria-describedby': errors.value[id] ? `${id}-error` : undefined, 'aria-invalid': !!errors.value[id] } }
function validateGroup(group: Section) {
  // Validate against saved values from other groups, never their unsaved edits.
  const savedSettings: Settings = JSON.parse(JSON.stringify(workspace.settings))
  const candidate = initialForm({ ...savedSettings, ...sectionPatch(group, form) })
  errors.value = validate(group, candidate, relayEligible.value, tunnels.value.map(item => item.mappingPort))
  if (group === 'relay' && !form.relay.enabled && tunnels.value.some(item => item.routeIds.length)) errors.value.relay = 'relayDisable'
  if (group === 'relay') {
    const endpointPort = Number(form.relay.endpoint.slice(form.relay.endpoint.lastIndexOf(':') + 1))
    if ([candidate.webPort, candidate.controlPort].includes(endpointPort)) errors.value.endpoint = 'conflict'
    if (endpointPort >= form.relay.mappingPool.start && endpointPort <= form.relay.mappingPool.end) errors.value.mappingPool = 'reserved'
    if (workspace.listeners.some(item => item.nodeId === workspace.settings.relayNodeId && (item.port === endpointPort || (item.port >= form.relay.mappingPool.start && item.port <= form.relay.mappingPool.end)))) errors.value.mappingPool = 'listenerConflict'
  }
  if (group === 'ports' && workspace.settings.relay?.enabled) {
    const relay = workspace.settings.relay, endpointPort = Number(relay.endpoint.slice(relay.endpoint.lastIndexOf(':') + 1))
    for (const field of ['webPort', 'controlPort'] as const) if (candidate[field] === endpointPort || (candidate[field] >= relay.mappingPool.start && candidate[field] <= relay.mappingPool.end)) errors.value[field] = 'controllerPort'
  }
  if (group === 'admin') {
    if (currentPassword.value !== (sessionSecrets.get('auth:admin-password') ?? 'demo-only')) errors.value.currentPassword = 'current'
    if (newPassword.value.length < 8 || newPassword.value.length > 128 || newPassword.value === currentPassword.value) errors.value.newPassword = 'password'
    if (newPassword.value !== confirmPassword.value) errors.value.confirmPassword = 'confirm'
  }
  if (Object.keys(errors.value).length) {
    requestAnimationFrame(() => document.querySelector<HTMLElement>('.invalid input, .invalid button')?.focus())
    return false
  }
  return true
}
function saveGroup(group: Section): boolean {
  feedback.value = ''; failed.value = false
  if (!validateGroup(group)) return false
  if (workspace.scenario === 'error') { failed.value = true; return false }
  if (group === 'admin') {
    sessionSecrets.set('auth:admin-password', newPassword.value)
    clearInputs(group); feedback.value = 'passwordChanged'; return true
  }
  if (group === 'logs') {
    if (form.logLevel === 'debug') {
      const oldLevel = workspace.settings.logLevel
      form.logs.previousLevel = oldLevel === 'debug' ? workspace.settings.logs?.previousLevel ?? 'warn' : oldLevel
      form.logs.debugUntil = new Date(Date.now() + form.logs.debugMinutes * 60000).toISOString()
    } else { delete form.logs.debugUntil; delete form.logs.previousLevel }
  }
  if (group === 'client') {
    if (!sessionSecrets.has('settings:paired-secret')) form.localClient.paired = false
    const saved = workspace.settings.localClient
    if (saved?.address !== form.localClient.address || saved.allowedOrigin !== form.localClient.allowedOrigin) {
      form.localClient.paired = false; sessionSecrets.delete('settings:paired-secret')
    }
  }
  const patch = sectionPatch(group, form)
  commands.updateSettings(patch)
  Object.assign(baseline.value, patch)
  clearInputs(group)
  feedback.value = 'saved'; message.success(text(feedback.value))
  return true
}
function discard(group = section.value) {
  Object.assign(form, sectionPatch(group, baseline.value)); clearInputs(group); errors.value = {}; feedback.value = ''; failed.value = false
}
function askToLeave(group: Section): Promise<boolean> {
  if (!isDirty(group)) return Promise.resolve(true)
  return new Promise(resolve => {
    let settled = false
    const finish = (value: boolean) => { if (!settled) { settled = true; resolve(value) } }
    const modal = dialog.warning({ title: text('dirtyTitle'), content: text('dirtyBody'),
      action: () => h('div', { class: 'actions' }, [
        h(NButton, { onClick: () => { finish(false); modal.destroy() } }, { default: () => text('stay') }),
        h(NButton, { onClick: () => { discard(group); finish(true); modal.destroy() } }, { default: () => text('discard') }),
        h(NButton, { type: 'primary', onClick: () => { if (saveGroup(group)) { finish(true); modal.destroy() } else { finish(false); modal.destroy() } } }, { default: () => group === 'admin' ? text('changePassword') : text('save') }),
      ]), onClose: () => finish(false), onMaskClick: () => finish(false), onAfterLeave: () => finish(false) })
  })
}
async function changeSection(value: Section) { await router.push(pageLocation('settings', {}, { section: value })) }
onBeforeRouteUpdate(async (to, from) => { if (to.query.section === from.query.section) return true; task.cancel(); return askToLeave(section.value) })
onBeforeRouteLeave(async () => { task.cancel(); for (const group of sections) if (!await askToLeave(group)) return false; return true })
watch(section, () => { errors.value = {}; feedback.value = ''; failed.value = false; clearInputs(); if (section.value === 'logs') restoreDebug(false) })
watch(resetEpoch, () => { task.cancel(); Object.assign(form, freshForm()); baseline.value = freshForm(); clearInputs(); errors.value = {}; feedback.value = ''; failed.value = false; locationQueriedAt.value = undefined; certificateReviewedAt.value = undefined; rotationOpen.value = false })
function unload(event: BeforeUnloadEvent) { if (sections.some(isDirty)) { event.preventDefault(); event.returnValue = '' } }
window.addEventListener('beforeunload', unload)
onBeforeUnmount(() => { clearInputs(); window.removeEventListener('beforeunload', unload) })
async function demoOperation(operation: 'location' | 'certificate' | 'pair') {
  if (task.state.value === 'running') return
  feedback.value = ''; failed.value = false
  const group = operation === 'pair' ? 'client' : operation === 'certificate' ? 'certificates' : 'location'
  if (!validateGroup(group)) return
  if (operation === 'pair' && !clientSecret.value.trim()) errors.value.clientSecret = 'secret'
  if (operation === 'location' && !locationKey.value.trim()) errors.value.locationKey = 'secret'
  if (operation === 'certificate') {
    if (form.certificates.method === 'dns01' && !dnsCredential.value.trim()) errors.value.dnsCredential = 'secret'
    if (form.certificates.method === 'import' && (!certificatePem.value.includes('-----BEGIN CERTIFICATE-----') || !certificatePem.value.includes('-----END CERTIFICATE-----') || !/-----BEGIN (?:RSA |EC )?PRIVATE KEY-----/.test(privateKeyPem.value) || !/-----END (?:RSA |EC )?PRIVATE KEY-----/.test(privateKeyPem.value))) errors.value.certificatePem = 'pem'
  }
  if (Object.keys(errors.value).length) return
  const operationEpoch = resetEpoch.value
  const signature = JSON.stringify(sectionPatch(group, form))
  const success = await task.run(workspace.scenario === 'error' || workspace.scenario === 'offline' ? 'error' : 'success')
  if (operationEpoch !== resetEpoch.value) return
  if (!success) { failed.value = task.state.value === 'error'; if (!failed.value) feedback.value = 'cancelled'; return }
  if (signature !== JSON.stringify(sectionPatch(group, form))) { feedback.value = 'changedRetry'; return }
  if (!validateGroup(group)) return
  if (operation === 'location') { locationQueriedAt.value = new Date().toISOString(); feedback.value = 'demoLocation' }
  if (operation === 'certificate') { certificateReviewedAt.value = new Date().toISOString(); feedback.value = 'certificateReviewed' }
  if (operation === 'pair') {
    if (!clientSecret.value.trim()) { errors.value.clientSecret = 'secret'; return }
    const secret = clientSecret.value
    if (!saveGroup('client')) return
    sessionSecrets.set('settings:paired-secret', secret)
    form.localClient.paired = true
    commands.updateSettings({ localClient: { ...form.localClient } }); baseline.value.localClient = { ...form.localClient }
    feedback.value = 'pairSuccess'
  }
}
function unpair() {
  task.cancel(); sessionSecrets.delete('settings:paired-secret'); clientSecret.value = ''
  const saved = initialForm(JSON.parse(JSON.stringify(workspace.settings))).localClient
  commands.updateSettings({ localClient: { ...saved, paired: false } })
  form.localClient.paired = false; baseline.value.localClient.paired = false; feedback.value = 'unpairSuccess'
}
function restoreDebug(notify: boolean) {
  const logs = workspace.settings.logs
  if (workspace.settings.logLevel !== 'debug' || !logs?.debugUntil) { if (notify) feedback.value = 'noDebug'; return }
  if (Date.parse(logs.debugUntil) > Date.now()) { if (notify) feedback.value = 'stillDebug'; return }
  const restored = { ...logs }; delete restored.debugUntil; delete restored.previousLevel
  const level = logs.previousLevel ?? 'warn'
  commands.updateSettings({ logLevel: level, logs: restored })
  if (!isDirty('logs')) { form.logLevel = level; form.logs = { ...restored }; baseline.value.logLevel = level; baseline.value.logs = { ...restored } }
  if (notify) feedback.value = 'restored'
}
if (section.value === 'logs') restoreDebug(false)
async function logout() {
  for (const group of sections) if (isDirty(group) && !await askToLeave(group)) return
  sessionSecrets.clear()
  const client = workspace.settings.localClient
  if (client) commands.updateSettings({ localClient: { ...client, paired: false } })
  await router.push(pageLocation('login', {}, { returnTo: router.resolve(pageLocation('settings', {}, { section: 'admin' })).fullPath }))
}
</script>
<template>
  <PageHeader :title="text('SettingsPage.title')" :description="text('SettingsPage.goal')" />
  <p class="draft-hint">{{ text('draftHint') }}</p>
  <div class="mobile-picker">
    <label id="section-picker-label">{{ text('section') }}</label>
    <NSelect :value="section" :options="options" aria-labelledby="section-picker-label" @update:value="changeSection" />
  </div>
  <div class="settings-layout">
    <nav class="section-nav" :aria-label="text('section')">
      <RouterLink v-for="item in sections" :key="item" :to="pageLocation('settings', {}, { section: item })" :class="{ selected: section === item }" :aria-current="section === item ? 'page' : undefined">
        {{ text(`sections.${item}`) }}<span v-if="isDirty(item)" :aria-label="text('unsaved')">•</span>
      </RouterLink>
    </nav>
    <div class="settings-content">
      <section class="panel" :aria-labelledby="`section-${section}`">
        <div class="section-heading"><h2 :id="`section-${section}`">{{ text(`sections.${section}`) }}</h2><NTag :type="dirty ? 'warning' : 'default'" size="small">{{ text(dirty ? 'unsaved' : 'clean') }}</NTag></div>
        <p>{{ text(`descriptions.${section}`) }}</p>
        <form @submit.prevent="saveGroup(section)">
          <template v-if="section === 'general'">
            <Field id="environment" :label="text('environment')"><NSelect v-model:value="form.environment" :options="environments" aria-labelledby="environment-label" /></Field>
            <p class="help">{{ text('regulationHint') }}</p>
            <div class="checkboxes"><NCheckbox v-model:checked="form.allowUdpTransport">{{ text('udpTransport') }}</NCheckbox><NCheckbox v-model:checked="form.requireUdpRelay">{{ text('udpBusiness') }}</NCheckbox></div>
            <p class="help">{{ text('udpHint') }}</p>
            <div class="field-grid"><Field v-for="key in (['measurementTtlHours', 'failureTtlMinutes'] as const)" :id="key" :key="key" :label="text(key)" :error="error(key)"><NInputNumber :value="form[key]" :min="1" :max="key === 'measurementTtlHours' ? 8760 : 1440" :precision="0" :input-props="inputProps(key)" @update:value="value => form[key] = value ?? 0" /></Field></div>
            <Field id="locale" :label="text('locale')"><NSelect :value="form.locale" :options="[{ value: 'zh-CN', label: '简体中文' }, { value: 'en', label: 'English' }]" aria-labelledby="locale-label" @update:value="changeLocale" /></Field>
            <p class="help">{{ text('dateFormat') }}</p>
          </template>
          <template v-else-if="section === 'ports'">
            <div class="field-grid"><Field v-for="key in portFields" :id="key" :key="key" :label="text(key)" :error="error(key) ?? (key === 'defaultPortStart' ? error('portPool') : undefined)"><NInputNumber :value="form[key]" :min="1" :max="65535" :precision="0" :input-props="inputProps(key)" @update:value="value => form[key] = value ?? 0" /></Field></div>
            <p class="help">{{ text('portHint') }}</p>
            <h3>{{ text('listeners') }}</h3>
            <ul v-if="workspace.nodes.length" class="object-list"><li v-for="node in workspace.nodes" :key="node.id"><RouterLink :to="pageLocation('node-detail', { nodeId: node.id }, { tab: 'protocols' })">{{ node.name }}</RouterLink><span>{{ workspace.listeners.filter(item => item.nodeId === node.id).map(item => `${item.protocol} · ${item.port}/${item.transport}`).join(', ') || '—' }}</span></li></ul>
            <p v-else>{{ text('noNodes') }} <RouterLink :to="pageLocation('nodes')">{{ text('nodes') }}</RouterLink></p>
          </template>
          <template v-else-if="section === 'relay'">
            <NAlert :type="relayEligible ? 'info' : 'warning'" :title="text('deployment')">{{ text(relayEligible ? 'both' : 'absent') }}<template v-if="relayNode"> · <RouterLink :to="entityLocation('node', relayNode.id)">{{ relayNode.name }}</RouterLink></template></NAlert>
            <div class="checkboxes"><NCheckbox v-model:checked="form.relay.enabled" :disabled="!relayEligible">{{ text('enableRelay') }}</NCheckbox></div>
            <p v-if="error('relay')" class="error" role="alert">{{ error('relay') }}</p>
            <Field id="endpoint" :label="text('endpoint')" :error="error('endpoint')"><NInput v-model:value="form.relay.endpoint" :disabled="!relayEligible" :input-props="inputProps('endpoint')" /></Field>
            <div class="field-grid"><Field id="mappingStart" :label="text('mappingStart')" :error="error('mappingPool')"><NInputNumber :value="form.relay.mappingPool.start" :min="1" :max="65535" :precision="0" :disabled="!relayEligible" :input-props="inputProps('mappingStart')" @update:value="value => form.relay.mappingPool.start = value ?? 0" /></Field><Field id="mappingEnd" :label="text('mappingEnd')"><NInputNumber :value="form.relay.mappingPool.end" :min="1" :max="65535" :precision="0" :disabled="!relayEligible" :input-props="inputProps('mappingEnd')" @update:value="value => form.relay.mappingPool.end = value ?? 0" /></Field></div>
            <p class="help">{{ text('relayHint') }}</p>
            <ul v-if="tunnels.length" class="object-list"><li v-for="tunnel in tunnels" :key="tunnel.id"><RouterLink :to="entityLocation('node', tunnel.remoteNodeId)">{{ workspace.nodes.find(node => node.id === tunnel.remoteNodeId)?.name ?? tunnel.remoteNodeId }}</RouterLink><code>127.0.0.1:{{ tunnel.mappingPort }} · tcp_tls</code></li></ul><p v-else>{{ text('noMappings') }}</p>
            <h3 v-if="affectedRoutes.length">{{ text('reverseRoutes') }}</h3><div class="related-links"><RouterLink v-for="item in affectedRoutes" :key="item.id" :to="entityLocation('route', item.id)">{{ item.name }}</RouterLink></div>
          </template>
          <template v-else-if="section === 'location'">
            <Field id="provider" :label="text('provider')" :error="error('provider')"><NSelect v-model:value="form.locationService.provider" :options="providers" aria-labelledby="provider-label" /></Field>
            <Field id="locationKey" :label="text('locationKey')" :error="error('locationKey')"><NInput v-model:value="locationKey" type="password" autocomplete="off" :maxlength="4096" :input-props="inputProps('locationKey')" /></Field>
            <p class="help">{{ text(locationKey ? 'memoryReady' : 'memoryEmpty') }}</p>
            <NCheckbox v-model:checked="form.locationService.preserveManual">{{ text('preserveManual') }}</NCheckbox><p class="help">{{ text('manualHint') }}</p>
            <NButton :loading="task.state.value === 'running'" @click="demoOperation('location')">{{ text('queryLocation') }}</NButton>
            <QueryStamp :queried-at="locationQueriedAt" />
            <p v-if="workspace.scenario === 'stale' && !locationQueriedAt" role="status">{{ text('stale') }}</p>
            <ul v-if="locationQueriedAt" class="object-list"><li v-for="node in workspace.nodes" :key="node.id"><RouterLink :to="entityLocation('node', node.id)">{{ node.name }}</RouterLink><span>{{ node.location }}<template v-if="node.city"> · {{ node.city }}</template><NTag v-if="node.manualLocation" size="small">{{ text('preserveManual') }}</NTag></span></li></ul>
            <p v-if="!workspace.nodes.length">{{ text('noNodes') }} <RouterLink :to="pageLocation('nodes')">{{ text('nodes') }}</RouterLink></p>
          </template>
          <template v-else-if="section === 'certificates'">
            <Field id="domain" :label="text('domain')" :error="error('domain')"><NInput v-model:value="form.certificates.domain" placeholder="controller.example.invalid" :maxlength="253" :input-props="inputProps('domain')" /></Field>
            <Field id="certificateMethod" :label="text('certificateMethod')"><NSelect v-model:value="form.certificates.method" :options="[{ value: 'dns01', label: text('dns01') }, { value: 'import', label: text('import') }]" aria-labelledby="certificateMethod-label" @update:value="() => { clearInputs('certificates'); certificateReviewedAt = undefined }" /></Field>
            <template v-if="form.certificates.method === 'dns01'">
              <Field id="dnsProvider" :label="text('dnsProvider')" :error="error('provider')"><NInput v-model:value="form.certificates.provider" :maxlength="80" :input-props="inputProps('dnsProvider')" /></Field>
              <Field id="dnsCredential" :label="text('dnsCredential')" :error="error('dnsCredential')"><NInput v-model:value="dnsCredential" type="password" autocomplete="off" :maxlength="4096" :input-props="inputProps('dnsCredential')" /></Field><p class="help">{{ text('dnsHint') }}</p>
            </template>
            <template v-else>
              <Field id="certificatePem" :label="text('certificatePem')" :error="error('certificatePem')"><NInput v-model:value="certificatePem" type="textarea" autocomplete="off" :maxlength="32768" :autosize="{ minRows: 3, maxRows: 6 }" :input-props="inputProps('certificatePem')" /></Field>
              <Field id="privateKeyPem" :label="text('privateKeyPem')"><NInput v-if="showPrivateKey" v-model:value="privateKeyPem" type="textarea" autocomplete="off" :maxlength="32768" :autosize="{ minRows: 3, maxRows: 6 }" :input-props="inputProps('privateKeyPem')" /><NInput v-else v-model:value="privateKeyPem" type="password" autocomplete="off" :maxlength="32768" :input-props="inputProps('privateKeyPem')" /><NCheckbox v-model:checked="showPrivateKey">{{ text('showPrivateKey') }}</NCheckbox></Field>
            </template>
            <p>{{ text('certStatus') }}: <NTag>{{ text('notChecked') }}</NTag></p><p v-if="workspace.scenario === 'stale'" class="help">{{ text('certExpired') }}</p>
            <div class="actions"><NButton :loading="task.state.value === 'running'" @click="demoOperation('certificate')">{{ text('inspectCertificate') }}</NButton><NButton @click="rotationOpen = true">{{ text('reviewRotation') }}</NButton></div>
            <QueryStamp :queried-at="certificateReviewedAt" /><h3>{{ text('scopes') }}</h3><p class="help">{{ text('scopeHint') }}</p>
          </template>
          <template v-else-if="section === 'client'">
            <NTag :type="clientPaired ? 'success' : 'default'">{{ text(clientPaired ? 'paired' : 'unpaired') }}</NTag>
            <Field id="address" :label="text('address')" :error="error('address')"><NInput v-model:value="form.localClient.address" :maxlength="200" :input-props="inputProps('address')" /></Field>
            <Field id="allowedOrigin" :label="text('allowedOrigin')" :error="error('allowedOrigin')"><NInput v-model:value="form.localClient.allowedOrigin" :maxlength="200" :input-props="inputProps('allowedOrigin')" /></Field>
            <Field id="clientSecret" :label="text('clientSecret')" :error="error('clientSecret')"><NInput v-model:value="clientSecret" type="password" autocomplete="off" :maxlength="4096" :input-props="inputProps('clientSecret')" /></Field><p class="help">{{ text('clientHint') }}</p>
            <NCheckbox v-model:checked="form.localClient.rawRtt">{{ text('rawRtt') }}</NCheckbox><p class="help">{{ text('rawRttHint') }}</p>
            <div class="actions"><NButton :loading="task.state.value === 'running'" @click="demoOperation('pair')">{{ text('pair') }}</NButton><NButton :disabled="!clientPaired" @click="unpair">{{ text('unpair') }}</NButton></div>
          </template>
          <template v-else-if="section === 'logs'">
            <Field id="logLevel" :label="text('logLevel')"><NSelect v-model:value="form.logLevel" :options="levels" aria-labelledby="logLevel-label" /></Field>
            <div class="field-grid"><Field v-for="item in logFields" :id="item.key" :key="item.key" :label="text(item.key)" :error="error(item.key)" :hint="`1–${item.max}`"><NInputNumber :value="form.logs[item.key]" :min="1" :max="item.max" :precision="0" :input-props="inputProps(item.key)" @update:value="value => form.logs[item.key] = value ?? 0" /></Field></div>
            <Field v-if="form.logLevel === 'debug'" id="debugMinutes" :label="text('debugMinutes')" :error="error('debugMinutes')"><NInputNumber :value="form.logs.debugMinutes" :min="1" :max="60" :precision="0" :input-props="inputProps('debugMinutes')" @update:value="value => form.logs.debugMinutes = value ?? 0" /></Field>
            <p v-if="workspace.settings.logs?.debugUntil">{{ text('debugUntil') }}: {{ date(workspace.settings.logs.debugUntil) }} · {{ text('previousLevel') }}: {{ text(`${workspace.settings.logs.previousLevel ?? 'warn'}Level`) }}</p>
            <p class="help">{{ text('debugHint') }}</p><NButton @click="restoreDebug(true)">{{ text('checkDebug') }}</NButton>
            <p><RouterLink :to="pageLocation('activity', {}, { tab: 'logs' })">{{ text('logActivity') }}</RouterLink></p>
          </template>
          <template v-else-if="section === 'admin'">
            <p>{{ text('adminName') }}: <strong>admin</strong></p><p class="help">{{ text('passwordHint') }}</p>
            <Field id="currentPassword" :label="text('currentPassword')" :error="error('currentPassword')"><NInput v-model:value="currentPassword" type="password" autocomplete="off" :maxlength="128" :input-props="inputProps('currentPassword')" /></Field>
            <Field id="newPassword" :label="text('newPassword')" :error="error('newPassword')"><NInput v-model:value="newPassword" type="password" autocomplete="off" :maxlength="128" :input-props="inputProps('newPassword')" /></Field>
            <Field id="confirmPassword" :label="text('confirmPassword')" :error="error('confirmPassword')"><NInput v-model:value="confirmPassword" type="password" autocomplete="off" :maxlength="128" :input-props="inputProps('confirmPassword')" /></Field>
          </template>
          <div class="form-footer"><NButton type="primary" attr-type="submit" :disabled="!dirty || task.state.value === 'running' || (section === 'relay' && !relayEligible)">{{ text(section === 'admin' ? 'changePassword' : 'save') }}</NButton><NButton :disabled="!dirty || task.state.value === 'running'" @click="discard()">{{ text('discard') }}</NButton></div>
        </form>
        <NAlert v-if="failed" type="error" class="feedback" role="alert">{{ text('failed') }}</NAlert>
        <p v-if="feedback" class="feedback" role="status">{{ text(feedback) }}</p>
        <div v-if="task.state.value === 'running'" class="actions" role="status">{{ text('queryRunning') }}<NButton @click="task.cancel">{{ text('cancel') }}</NButton></div>
        <div v-if="section === 'admin'" class="logout"><NButton secondary @click="logout">{{ text('logout') }}</NButton><p class="help">{{ text('logoutHint') }}</p></div>
      </section>
      <ControllerInfo v-if="section === 'general'" />
    </div>
  </div>
  <NModal v-model:show="rotationOpen" preset="card" :title="text('rotationTitle')" class="rotation-dialog" :style="{ width: 'min(600px, calc(100vw - 32px))' }">
    <p>{{ text('rotationHint') }}</p><ul v-if="workspace.nodes.length" class="object-list"><li v-for="node in workspace.nodes" :key="node.id"><RouterLink :to="pageLocation('node-detail', { nodeId: node.id }, { tab: 'protocols' })" @click="rotationOpen = false">{{ node.name }}</RouterLink><span>{{ node.protocolDefaults.filter(item => item.enabled && ['trojan', 'hysteria2'].includes(item.protocol)).map(item => item.protocol).join(', ') || text('notChecked') }}</span></li></ul><p v-else>{{ text('noNodes') }}</p><NButton @click="rotationOpen = false">{{ text('close') }}</NButton>
  </NModal>
</template>
<style scoped>
.draft-hint, .help { color: var(--color-muted); font-size: 13px; line-height: 1.65; } .draft-hint { max-width: 850px; margin-bottom: 24px; }
.settings-layout { display: grid; grid-template-columns: 180px minmax(0, 1fr); gap: 24px; align-items: start; } .settings-content { min-width: 0; max-width: 880px; } .section-nav { gap: 4px; position: sticky; top: 20px; } .section-nav a { display: flex; align-items: center; justify-content: space-between; min-height: 44px; padding: 12px 16px; border-radius: 8px; text-decoration: none; color: var(--color-muted); } .section-nav a.selected { background: #eaf3ed; color: var(--color-primary); font-weight: 600; } .section-heading { display: flex; gap: 12px; align-items: center; justify-content: space-between; flex-wrap: wrap; margin-bottom: 12px; } .section-heading h2 { margin: 0; } .field-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 24px; } .field-grid :deep(.n-input-number) { width: 100%; } .checkboxes { display: grid; gap: 14px; margin: 20px 0 12px; } .object-list { list-style: none; margin: 16px 0; padding: 0; } .object-list li { padding: 12px 0; border-bottom: 1px solid var(--color-border); display: flex; flex-wrap: wrap; justify-content: space-between; gap: 8px 16px; } .object-list li span { color: var(--color-muted); overflow-wrap: anywhere; } .related-links { display: flex; flex-wrap: wrap; gap: 12px 20px; } .related-links a { min-height: 44px; display: inline-flex; align-items: center; } .form-footer { display: flex; flex-wrap: wrap; gap: 12px; border-top: 1px solid var(--color-border); padding-top: 24px; margin-top: 28px; } .logout { border-top: 1px solid var(--color-border); margin-top: 24px; padding-top: 24px; } .feedback { margin-top: 20px; } .error { color: #ab332c; } .mobile-picker { display: none; margin-bottom: 24px; } h3 { margin: 24px 0 12px; font-size: 14px; } code { overflow-wrap: anywhere; }
@media (max-width: 1000px) { .settings-layout { grid-template-columns: 145px minmax(0, 1fr); gap: 16px; } .section-nav a { padding: 12px; } }
@media (max-width: 760px) { .settings-layout { display: block; } .section-nav { display: none; } .mobile-picker { display: block; } .field-grid { grid-template-columns: 1fr; } .form-footer :deep(.n-button) { flex: 1; min-height: 44px; } }
</style>
