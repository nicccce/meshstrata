<script setup lang="ts">
import { computed, h, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NInput, useDialog, useMessage } from 'naive-ui'
import { pageLocation, entityLocation } from '../../../app/router'
import { resetEpoch, useWorkspace } from '../../../demo/workspace'
import { useDraft } from '../../../composables/useDraft'
import { useSimulatedTask } from '../../../composables/useSimulatedTask'
import { useFormat } from '../../../composables/useFormat'
import type { Draft, Node } from '../../../domain'
import { automaticLocation, enrollmentSteps, environments, newNodeForm, protocolDefaults, validAccessAddress, type EnrollmentStep } from '../node-model'

const props = defineProps<{ draftId: string }>()
const { t } = useI18n()
const tr = (key: string, params: Record<string, string | number> = {}) => t(`features.nodes.${key}`, params)
const route = useRoute(), router = useRouter(), message = useMessage()
const { workspace, commands, sessionSecrets } = useWorkspace()
const { date } = useFormat()
const initial: Draft = {
  id: props.draftId, feature: 'nodes', objectId: `enrolled-${props.draftId}`, name: '', nodeIds: [], routeIds: [],
  step: 'basics', updatedAt: new Date().toISOString(), data: { node: newNodeForm(workspace.settings.environment) },
}
const { value: draft, dirty, save, discard } = useDraft(props.draftId, initial)
const dialog = useDialog()
// useDraft guards route leave. Switching draft IDs reuses the same matched
// route, so guard that transition here too; step changes remain uninterrupted.
onBeforeRouteUpdate(to => {
  if (to.query.draftId === props.draftId || !dirty.value) return true
  return new Promise<boolean>(resolve => {
    let settled = false
    const finish = (leave: boolean) => { if (!settled) { settled = true; resolve(leave) } }
    const instance = dialog.warning({
      title: t('common.dirty'), content: t('common.dirtyBody'),
      action: () => h('div', { class: 'actions' }, [
        h(NButton, { onClick: () => { finish(false); instance.destroy() } }, { default: () => t('common.stay') }),
        h(NButton, { onClick: () => { discard(); finish(true); instance.destroy() } }, { default: () => t('common.discard') }),
        h(NButton, { onClick: () => { save(); finish(true); instance.destroy() } }, { default: () => t('common.saveDraft') }),
      ]), onClose: () => finish(false), onMaskClick: () => finish(false), onAfterLeave: () => finish(false),
    })
  })
})
if (!draft.value.data?.node) draft.value.data = { node: newNodeForm(workspace.settings.environment) }
const form = computed(() => draft.value.data?.node ?? initial.data!.node!)
const step = computed<EnrollmentStep>(() => {
  const requested = typeof route.query.step === 'string' ? route.query.step : draft.value.step
  return enrollmentSteps.find(item => item === requested) ?? 'basics'
})
watch(step, value => { draft.value.step = value }, { immediate: true })
const task = useSimulatedTask()
const busy = computed(() => task.state.value === 'running')
const finishing = ref(false)
const showValidation = ref(false)
const statusMessage = ref('')
const copyFeedback = ref('')
const relay = computed(() => workspace.nodes.find(node => node.id === workspace.settings.relayNodeId))
// Legacy demo snapshots predate Settings.relay. Their explicit seeded tunnel is
// evidence that Relay was enabled in that snapshot, not a real availability test.
const relayEnabled = computed(() => workspace.settings.relay?.enabled ?? workspace.tunnels.some(tunnel => tunnel.relayNodeId === workspace.settings.relayNodeId))
const relayConditions = computed(() => [
  { key: 'relayExists', passed: !!relay.value && !relay.value.removedAt },
  { key: 'relayBoth', passed: relay.value?.role === 'both' && relay.value.mode === 'direct' },
  { key: 'relayEnabled', passed: relayEnabled.value },
  { key: 'relayOnline', passed: relay.value?.control === 'online' },
])
const relayReady = computed(() => relayConditions.value.every(condition => condition.passed))
const validation = computed(() => {
  const issues: { field: string; key: string }[] = []
  const name = draft.value.name.trim()
  if (!name) issues.push({ field: 'enroll-name', key: 'nameRequired' })
  else if (name.length > 80) issues.push({ field: 'enroll-name', key: 'nameLong' })
  else if (workspace.nodes.some(node => node.name.trim().toLocaleLowerCase() === name.toLocaleLowerCase())) issues.push({ field: 'enroll-name', key: 'nameDuplicate' })
  if (form.value.mode === 'direct' && !validAccessAddress(form.value.address)) issues.push({ field: 'enroll-address', key: 'addressInvalid' })
  if (form.value.location.length > 80 || (form.value.city?.length ?? 0) > 80) issues.push({ field: 'enroll-country', key: 'locationLong' })
  if (form.value.mode === 'reverse' && !relayReady.value) issues.push({ field: 'enroll-relay', key: 'relayInvalid' })
  return issues
})
function fieldError(field: string) { return showValidation.value ? validation.value.find(issue => issue.field === field) : undefined }
function focusField(field: string) { document.getElementById(field)?.focus() }

const tokenKey = `${props.draftId}:node-enrollment`
const token = computed(() => sessionSecrets.get(tokenKey) ?? '')
const expiresAt = ref('')
const tokenExpired = ref(false)
const controllerAddress = computed(() => `https://controller.example.invalid:${workspace.settings.controlPort ?? 9443}`)
type CheckState = 'pending' | 'running' | 'success' | 'failed' | 'expiredState'
const checks = ref<Record<'registration' | 'identity' | 'versions' | 'location', CheckState>>({ registration: 'pending', identity: 'pending', versions: 'pending', location: 'pending' })
const checkNames = ['registration', 'identity', 'versions', 'location'] as const
const verified = computed(() => checkNames.every(key => checks.value[key] === 'success'))
const hasFailure = computed(() => checkNames.some(key => checks.value[key] === 'failed'))
const outcome = ref<'success' | 'failure' | 'expired'>(workspace.scenario === 'error' || workspace.scenario === 'offline' ? 'failure' : workspace.scenario === 'stale' ? 'expired' : 'success')
function clearToken() { sessionSecrets.delete(tokenKey); expiresAt.value = '' }
function resetChecks() { checks.value = { registration: 'pending', identity: 'pending', versions: 'pending', location: 'pending' } }
function invalidate() {
  const hadProgress = !!token.value || checkNames.some(key => checks.value[key] !== 'pending')
  task.cancel(); clearToken(); tokenExpired.value = false; resetChecks()
  if (hadProgress) statusMessage.value = 'upstreamChanged'
}
watch(() => JSON.stringify({ name: draft.value.name, node: form.value }), invalidate, { flush: 'sync' })
watch(() => JSON.stringify({ relayId: workspace.settings.relayNodeId, ready: relayReady.value }), () => {
  if (form.value.mode === 'reverse') invalidate()
})
watch(resetEpoch, () => { task.cancel(); clearToken(); resetChecks(); tokenExpired.value = false; showValidation.value = false; statusMessage.value = '' })
onBeforeUnmount(clearToken)

function saveDraft() {
  draft.value.updatedAt = new Date().toISOString()
  save()
  message.success(tr('wizard.saved'))
}
function discardChanges() {
  discard(); showValidation.value = false
  void router.replace(pageLocation('node-new', {}, { draftId: props.draftId, step: draft.value.step || 'basics' }))
  message.info(tr('wizard.discarded'))
}
function goStep(target: EnrollmentStep) {
  if (busy.value || finishing.value) return
  if (target !== 'basics' && validation.value.length) {
    showValidation.value = true
    void router.replace(pageLocation('node-new', {}, { draftId: props.draftId, step: 'basics' }))
    return
  }
  draft.value.step = target
  void router.push(pageLocation('node-new', {}, { draftId: props.draftId, step: target }))
}
function continueStep() {
  if (step.value === 'basics') goStep('install')
  else if (step.value === 'install') {
    if (!token.value || !expiresAt.value || Date.parse(expiresAt.value) <= Date.now()) {
      statusMessage.value = 'tokenRequired'
      if (token.value) { tokenExpired.value = true; clearToken() }
      return
    }
    goStep('verify')
  }
}
function generateToken() {
  if (validation.value.length) { goStep('basics'); showValidation.value = true; return }
  clearToken(); resetChecks(); tokenExpired.value = false
  sessionSecrets.set(tokenKey, `DEMO-NOT-A-CREDENTIAL-${crypto.randomUUID()}`)
  expiresAt.value = new Date(Date.now() + 15 * 60000).toISOString()
  statusMessage.value = ''; copyFeedback.value = ''
}
async function copy(value: string, item: string) {
  try {
    if (!navigator.clipboard?.writeText) throw Error('CLIPBOARD_UNAVAILABLE')
    await navigator.clipboard.writeText(value)
    copyFeedback.value = tr('wizard.copied', { item: tr(`wizard.${item}`) })
  } catch { copyFeedback.value = tr('wizard.copyFailed') }
}
function openRelaySettings() { saveDraft(); void router.push(pageLocation('settings', {}, { section: 'relay' })) }
function regenerate() { goStep('install'); generateToken() }
async function simulateRegistration(retry = false) {
  if (busy.value || finishing.value) return
  showValidation.value = true
  if (validation.value.length) { goStep('basics'); return }
  if (!token.value || !expiresAt.value || Date.parse(expiresAt.value) <= Date.now()) {
    tokenExpired.value = !!expiresAt.value || tokenExpired.value
    checks.value.registration = tokenExpired.value ? 'expiredState' : 'pending'
    statusMessage.value = 'tokenRequired'; clearToken()
    return
  }
  statusMessage.value = ''
  const generation = resetEpoch.value
  // A retry preserves already successful checks and retries the failed suffix.
  if (!retry) resetChecks()
  checkNames.forEach(key => { if (checks.value[key] !== 'success') checks.value[key] = 'running' })
  const requested = outcome.value
  const completed = await task.run(requested === 'failure' ? 'error' : 'success', 850)
  if (generation !== resetEpoch.value || task.state.value === 'cancelled') return
  if (Date.parse(expiresAt.value) <= Date.now() || requested === 'expired') {
    resetChecks(); checks.value.registration = 'expiredState'; tokenExpired.value = true
    clearToken(); statusMessage.value = 'expired'
    return
  }
  if (!completed) {
    checks.value = { registration: 'success', identity: 'failed', versions: 'pending', location: 'pending' }
    statusMessage.value = 'identityFailure'
    return
  }
  checkNames.forEach(key => { checks.value[key] = 'success' })
  statusMessage.value = 'registrationSuccess'
}
function cancelSimulation() { task.cancel(); resetChecks(); statusMessage.value = 'registrationCancelled' }
async function finish() {
  if (!verified.value || busy.value || finishing.value) return
  if (validation.value.length) { goStep('basics'); showValidation.value = true; return }
  if (!token.value || Date.parse(expiresAt.value) <= Date.now()) { tokenExpired.value = true; resetChecks(); clearToken(); statusMessage.value = 'expired'; return }
  const id = draft.value.objectId || `enrolled-${props.draftId}`
  if (workspace.nodes.some(node => node.id === id)) { statusMessage.value = 'duplicateId'; return }
  finishing.value = true
  const now = new Date().toISOString()
  const manual = !!(form.value.location.trim() || form.value.city?.trim())
  const detected = automaticLocation({ id, location: '', city: '' })
  const node: Node = {
    id, name: draft.value.name.trim(), updatedAt: now, mode: form.value.mode, role: 'node',
    ...(form.value.mode === 'direct' ? { address: form.value.address.trim() } : {}),
    controlAddress: '192.0.2.42', observedSourceIp: '192.0.2.42',
    location: form.value.location.trim() || detected.location, city: form.value.city?.trim() || (manual ? '' : detected.city), manualLocation: manual,
    environment: form.value.environment ?? workspace.settings.environment ?? 'outside_mainland_china', tags: [],
    control: 'online', tunnel: 'unknown', verification: 'not_checked',
    agentVersion: 'demo-agent', mihomoVersion: 'demo-pinned', lastConfirmedAt: now, queriedAt: now,
    protocolDefaults: protocolDefaults(form.value.mode, workspace.settings.defaultPortStart),
    targetDns: { revision: 1, upstreams: [...workspace.settings.targetDns] },
    capabilities: { tcp: 'not_checked', udp: form.value.mode === 'reverse' ? 'failed' : 'not_checked' },
  }
  commands.upsert('nodes', node)
  // Mark this editor clean before leaving; the completed enrollment is no longer
  // a resumable draft. useDraft remains the sole dirty-state owner.
  save(); commands.remove('drafts', props.draftId); clearToken()
  message.success(tr('wizard.finished'))
  await router.push(pageLocation('node-detail', { nodeId: id }, { tab: 'protocols' }))
  finishing.value = false
}
</script>

<template>
  <div class="enrollment">
    <nav class="steps" :aria-label="tr('wizard.summary')">
      <NButton v-for="(item, index) in enrollmentSteps" :key="item" :type="step === item ? 'primary' : 'default'" :aria-current="step === item ? 'step' : undefined" :disabled="busy || finishing" @click="goStep(item)"><span class="step-number">{{ index + 1 }}</span>{{ tr(`wizard.${item}`) }}</NButton>
    </nav>
    <div class="wizard-layout">
      <form class="wizard-main" @submit.prevent="continueStep">
        <NAlert v-if="showValidation && validation.length" type="error" :show-icon="false" class="notice" :title="tr('wizard.validationTitle')">
          <ul class="validation-list"><li v-for="issue in validation" :key="issue.key"><button type="button" class="text-button" @click="focusField(issue.field)">{{ tr(`wizard.${issue.key}`) }}</button></li></ul>
        </NAlert>
        <section v-if="step === 'basics'" class="panel">
          <h2>{{ tr('wizard.step', { number: 1, name: tr('wizard.basics') }) }}</h2>
          <div class="field"><label for="enroll-name">{{ tr('wizard.name') }}</label><NInput v-model:value="draft.name" :maxlength="100" :placeholder="tr('wizard.namePlaceholder')" :status="fieldError('enroll-name') ? 'error' : undefined" :input-props="{ id: 'enroll-name', 'aria-required': true, 'aria-invalid': !!fieldError('enroll-name'), 'aria-describedby': 'name-error' }" /><p v-if="fieldError('enroll-name')" id="name-error" class="field-error">{{ tr(`wizard.${fieldError('enroll-name')!.key}`) }}</p></div>
          <fieldset class="mode-options"><legend>{{ tr('list.mode') }}</legend><label v-for="item in (['direct', 'reverse'] as const)" :key="item" :class="{ chosen: form.mode === item }"><input v-model="form.mode" type="radio" :value="item" name="connection-mode" />{{ tr(`mode.${item}`) }}</label></fieldset>
          <div v-if="form.mode === 'direct'" class="field"><label for="enroll-address">{{ tr('wizard.address') }}</label><NInput v-model:value="form.address" :maxlength="253" :placeholder="tr('wizard.addressPlaceholder')" :status="fieldError('enroll-address') ? 'error' : undefined" :input-props="{ id: 'enroll-address', 'aria-required': true, 'aria-invalid': !!fieldError('enroll-address'), 'aria-describedby': 'address-help address-error', autocomplete: 'off' }" /><p id="address-help" class="help">{{ tr('wizard.addressHelp') }}</p><p v-if="fieldError('enroll-address')" id="address-error" class="field-error">{{ tr(`wizard.${fieldError('enroll-address')!.key}`) }}</p></div>
          <div v-else id="enroll-relay" class="relay-card" tabindex="-1">
            <h3>{{ tr('wizard.relayTitle') }}</h3><p>{{ tr('wizard.reverseHelp') }}</p>
            <RouterLink v-if="relay" :to="entityLocation('node', relay.id)">{{ relay.name }}</RouterLink><strong v-else>{{ tr('wizard.relayMissing') }}</strong>
            <ul class="relay-conditions"><li v-for="condition in relayConditions" :key="condition.key"><span>{{ tr(`wizard.${condition.key}`) }}</span><strong :class="{ 'field-error': !condition.passed }">{{ tr(`wizard.${condition.passed ? 'conditionPassed' : 'conditionFailed'}`) }}</strong></li></ul>
            <p class="help">{{ tr('wizard.relayFixed') }}</p><p class="help">{{ tr('wizard.reverseLimits') }}</p><NButton v-if="!relayReady" @click="openRelaySettings">{{ tr('wizard.settings') }}</NButton>
          </div>
          <div class="field"><label for="enroll-environment">{{ tr('list.environment') }}</label><select id="enroll-environment" v-model="form.environment"><option v-for="item in environments" :key="item" :value="item">{{ tr(`environment.${item}`) }}</option></select></div>
          <div class="field-grid"><div class="field"><label for="enroll-country">{{ tr('wizard.country') }}</label><NInput v-model:value="form.location" :maxlength="80" :input-props="{ id: 'enroll-country', 'aria-describedby': 'manual-help' }" /></div><div class="field"><label for="enroll-city">{{ tr('wizard.city') }}</label><NInput v-model:value="form.city" :maxlength="80" :input-props="{ id: 'enroll-city', 'aria-describedby': 'manual-help' }" /></div></div>
          <p id="manual-help" class="help">{{ tr('wizard.manualHelp') }}</p>
        </section>
        <section v-else-if="step === 'install'" class="panel">
          <h2>{{ tr('wizard.step', { number: 2, name: tr('wizard.install') }) }}</h2>
          <p>{{ tr('wizard.installHelp') }}</p>
          <div class="field"><label for="example-controller">{{ tr('wizard.controller') }}</label><div class="copy-row"><NInput :value="controllerAddress" readonly :input-props="{ id: 'example-controller' }" /><NButton @click="copy(controllerAddress, 'controller')">{{ tr('wizard.copy') }}</NButton></div></div>
          <div class="field"><label for="example-fingerprint">{{ tr('wizard.fingerprint') }}</label><div class="copy-row"><NInput :value="tr('wizard.exampleFingerprint')" type="textarea" readonly :autosize="{ minRows: 2, maxRows: 4 }" :input-props="{ id: 'example-fingerprint' }" /><NButton @click="copy(tr('wizard.exampleFingerprint'), 'fingerprint')">{{ tr('wizard.copy') }}</NButton></div></div>
          <div class="field"><label for="example-token">{{ tr('wizard.token') }}</label><div class="copy-row"><NInput :value="token" readonly :placeholder="tr('wizard.tokenEmpty')" :input-props="{ id: 'example-token', autocomplete: 'off', 'aria-describedby': 'token-help' }" /><NButton :disabled="!token" @click="copy(token, 'token')">{{ tr('wizard.copy') }}</NButton></div><p v-if="expiresAt" class="help">{{ tr('wizard.expires', { time: date(expiresAt) }) }}</p><p v-if="tokenExpired" class="field-error">{{ tr('wizard.expired') }}</p><p id="token-help" class="help">{{ tr('wizard.tokenHelp') }}</p><NButton @click="generateToken">{{ tr(`wizard.${token ? 'regenerate' : 'generate'}`) }}</NButton></div>
          <div class="field"><label for="install-example">{{ tr('wizard.installTitle') }}</label><NInput :value="tr('wizard.installExample')" type="textarea" readonly :autosize="{ minRows: 9 }" :input-props="{ id: 'install-example', spellcheck: false }" /><NButton class="copy-example" @click="copy(tr('wizard.installExample'), 'example')">{{ tr('wizard.copy') }}</NButton></div>
          <p v-if="copyFeedback" role="status" class="help">{{ copyFeedback }}</p>
        </section>
        <section v-else class="panel">
          <h2>{{ tr('wizard.step', { number: 3, name: tr('wizard.verify') }) }}</h2>
          <p>{{ tr('wizard.verifyHelp') }}</p><p class="help">{{ tr('wizard.sourceHelp') }}</p>
          <NAlert v-if="!token && !tokenExpired" type="info" :show-icon="false" class="notice">{{ tr('wizard.tokenLost') }}</NAlert>
          <div class="field"><label for="registration-outcome">{{ tr('wizard.outcome') }}</label><select id="registration-outcome" v-model="outcome" :disabled="busy || verified"><option value="success">{{ tr('wizard.outcomeSuccess') }}</option><option value="failure">{{ tr('wizard.outcomeFailure') }}</option><option value="expired">{{ tr('wizard.outcomeExpired') }}</option></select></div>
          <ol class="verification" aria-live="polite" :aria-busy="busy"><li v-for="key in checkNames" :key="key"><div><strong>{{ tr(`wizard.${key}`) }}</strong><small v-if="checks[key] === 'success' && key === 'versions'">{{ tr('wizard.metadata') }}</small><small v-if="checks[key] === 'success' && key === 'location'">{{ tr(`wizard.${form.location.trim() || form.city?.trim() ? 'manualPreserved' : 'autoApplied'}`) }}</small></div><span :class="['check-state', checks[key]]">{{ tr(`wizard.${checks[key]}`) }}</span></li></ol>
          <div class="actions"><NButton v-if="!verified && !tokenExpired" :disabled="busy || !token" @click="simulateRegistration(hasFailure)">{{ tr(`wizard.${hasFailure ? 'retry' : 'register'}`) }}</NButton><NButton v-if="!token || tokenExpired" :disabled="busy" @click="regenerate">{{ tr('wizard.retryExpired') }}</NButton><NButton v-if="busy" @click="cancelSimulation">{{ tr('wizard.cancelTask') }}</NButton></div>
        </section>
        <NAlert v-if="statusMessage" :type="verified ? 'success' : ['identityFailure', 'expired', 'tokenRequired', 'duplicateId'].includes(statusMessage) ? 'warning' : 'info'" :show-icon="false" class="notice" role="status">{{ tr(`wizard.${statusMessage}`) }}</NAlert>
        <div class="wizard-footer">
          <NButton :disabled="busy || finishing" @click="router.push(pageLocation('nodes'))">{{ tr('wizard.cancel') }}</NButton>
          <div class="actions"><NButton v-if="step !== 'basics'" :disabled="busy || finishing" @click="goStep(step === 'verify' ? 'install' : 'basics')">{{ tr('wizard.back') }}</NButton><NButton v-if="step !== 'verify'" type="primary" attr-type="submit" :disabled="busy || finishing">{{ tr('wizard.next') }}</NButton><NButton v-else type="primary" :disabled="!verified || busy || finishing" :loading="finishing" @click="finish">{{ tr('wizard.finish') }}</NButton></div>
        </div>
      </form>
      <aside class="panel summary" :aria-label="tr('wizard.summary')">
        <h2>{{ tr('wizard.summary') }}</h2><dl><dt>{{ tr('wizard.name') }}</dt><dd>{{ draft.name.trim() || tr('list.untitled') }}</dd><dt>{{ tr('list.mode') }}</dt><dd>{{ tr(`mode.${form.mode}`) }}</dd><dt>{{ tr('wizard.address') }}</dt><dd>{{ form.mode === 'direct' ? form.address || tr('list.unknown') : tr('list.noAddress') }}</dd><dt v-if="form.mode === 'reverse'">{{ tr('wizard.relayTitle') }}</dt><dd v-if="form.mode === 'reverse'">{{ relay?.name || tr('wizard.relayMissing') }}</dd><dt>{{ tr('list.location') }}</dt><dd>{{ [form.location, form.city].filter(Boolean).join(' · ') || tr('wizard.locationAuto') }}</dd><dt>{{ tr('list.environment') }}</dt><dd>{{ tr(`environment.${form.environment || 'outside_mainland_china'}`) }}</dd></dl>
        <p class="draft-state" aria-live="polite">{{ tr(`wizard.${dirty ? 'unsaved' : 'clean'}`) }}</p><div class="draft-actions"><NButton :disabled="busy || finishing" @click="saveDraft">{{ tr('wizard.save') }}</NButton><NButton :disabled="!dirty || busy || finishing" @click="discardChanges">{{ tr('wizard.discard') }}</NButton></div>
        <p v-if="form.mode === 'reverse'" class="help">{{ tr('wizard.reverseLimits') }}</p>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.steps { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; margin-bottom:24px; }.steps :deep(button) { min-height:48px; }.step-number { display:inline-flex; align-items:center; justify-content:center; width:24px; height:24px; border:1px solid currentColor; border-radius:50%; margin-right:12px; }
.wizard-layout { display:grid; grid-template-columns:minmax(0,880px) minmax(240px,300px); align-items:start; gap:24px; }.wizard-main { min-width:0; }.summary { position:sticky; top:24px; padding:24px; }.summary dd { margin-bottom:20px; font-family:inherit; }.summary dt { font-size:12px; }.summary h2 { font-size:16px; }
.field { margin-top:24px; }.field-grid { display:grid; grid-template-columns:1fr 1fr; gap:16px; }.help { font-size:12px; line-height:1.7; margin-top:8px; }.field-error { color:#a1372d; font-size:12px; line-height:1.6; }.notice { margin-bottom:24px; }.validation-list { padding-left:20px; margin:4px 0; }.validation-list button { text-align:left; padding:8px 0; }
.mode-options { border:0; padding:0; display:flex; flex-wrap:wrap; gap:12px; margin:24px 0 0; }.mode-options legend { margin-bottom:8px; }.mode-options label { display:flex; align-items:center; gap:8px; padding:14px 18px; border:1px solid var(--color-border); border-radius:8px; margin:0; cursor:pointer; }.mode-options label.chosen { border-color:var(--color-primary); background:#eff7f1; }.mode-options input { accent-color:var(--color-primary); }
select { width:100%; min-height:40px; border:1px solid var(--color-border); border-radius:4px; padding:8px 12px; background:white; font:inherit; color:var(--color-text); }.relay-card { padding:20px; background:#f5f8f4; border:1px solid var(--color-border); border-radius:8px; margin-top:24px; }.relay-card h3 { margin:0; font-size:15px; }.relay-conditions { list-style:none; padding:0; }.relay-conditions li { display:flex; justify-content:space-between; gap:16px; padding:8px 0; font-size:12px; }.relay-conditions strong { flex-shrink:0; }
.copy-row { display:flex; align-items:start; gap:8px; }.copy-row :deep(.n-input) { min-width:0; }.copy-row :deep(button) { flex-shrink:0; }.copy-example { margin-top:12px; }
.verification { list-style:none; padding:0; margin-top:24px; }.verification li { display:flex; align-items:center; justify-content:space-between; gap:16px; padding:20px 0; border-bottom:1px solid var(--color-border); }.verification small { display:block; margin-top:6px; color:var(--color-muted); }.check-state { background:#f1f3f0; padding:5px 9px; border-radius:5px; font-size:12px; white-space:nowrap; }.check-state.success { color:#206553; background:#eaf3ed; }.check-state.failed,.check-state.expiredState { color:#a1372d; background:#fff0e9; }
.wizard-footer { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:12px; margin-bottom:24px; }.wizard-footer .actions { margin:0; }.draft-actions { display:flex; flex-direction:column; align-items:stretch; gap:12px; }.draft-state { font-size:12px; }
@media(max-width:1080px) { .wizard-layout { grid-template-columns:minmax(0,1fr); }.summary { position:static; }.summary dl { display:grid; grid-template-columns:minmax(100px,1fr) 2fr; gap:12px; }.summary dd,.summary dt { margin:0; }.draft-actions { flex-direction:row; flex-wrap:wrap; } }
@media(max-width:600px) { .steps { gap:6px; }.steps :deep(.n-button__content) { white-space:normal; }.step-number { width:20px; height:20px; margin-right:6px; flex-shrink:0; }.field-grid { grid-template-columns:1fr; gap:0; }.copy-row { flex-direction:column; }.verification li { align-items:start; }.wizard-footer :deep(button),.field :deep(button),.draft-actions :deep(button),select { min-height:44px; }.wizard-footer { align-items:stretch; }.wizard-footer .actions { flex:1; justify-content:flex-end; }.relay-conditions li { flex-direction:column; gap:4px; }.summary { padding:20px 16px; } }
</style>
