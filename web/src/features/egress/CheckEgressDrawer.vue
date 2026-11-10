<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { NAlert, NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NSelect, useDialog, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { entityLocation, pageLocation } from '../../app/router'
import { useWorkspace, resetEpoch } from '../../demo/workspace'
import { useSimulatedTask } from '../../composables/useSimulatedTask'
import { useFormat } from '../../composables/useFormat'
import type { EgressCheck } from '../../domain'
import type { CheckEgressDrawerProps, CheckEgressDrawerEmits } from './types'
import { newId, parseNetwork } from './logic'
const props = defineProps<CheckEgressDrawerProps>(); const emit = defineEmits<CheckEgressDrawerEmits>()
const { t } = useI18n(); const tr = (key: string) => t(`features.egress.${key}`)
const { workspace, commands, localClientPaired } = useWorkspace(); const task = useSimulatedTask(); const localTask = useSimulatedTask(); const format = useFormat(); const dialog = useDialog(); const message = useMessage()
const route = computed(() => workspace.routes.find(r => r.id === props.routeId)); const version = ref('published')
const revision = computed(() => version.value === 'published' ? route.value?.published : route.value?.draft)
const policy = computed(() => workspace.policies.find(p => p.id === revision.value?.policyId))
const terminal = computed(() => workspace.nodes.find(n => n.id === revision.value?.nodeIds.at(-1)))
const scope = ref<'route' | 'target_only'>('route'); const targetId = ref<string | null>(null); const entryId = ref<string | null>(null)
const entries = computed(() => workspace.entries.filter(e => e.routeId === props.routeId && (!props.subscriptionId || e.subscriptionId === props.subscriptionId)))
const entry = computed(() => workspace.entries.find(e => e.id === entryId.value))
const effectiveSubscription = computed(() => props.subscriptionId ?? entry.value?.subscriptionId)
const targets = computed(() => workspace.targets.filter(target => target.nodeId === terminal.value?.id))
const target = computed(() => targets.value.find(item => item.id === targetId.value))
const kinds: EgressCheck['kind'][] = ['destination', 'egress_ip', 'dns_path', 'client_coverage']
const enabled = ref<EgressCheck['kind'][]>(['destination', 'egress_ip', 'dns_path', 'client_coverage'])
const destination = ref('https://service.example.invalid/'); const echo = ref('https://echo.example.invalid/ip'); const domain = ref('service.example.invalid'); const outcome = ref('success')
const outcomeOptions = computed(() => ['success', 'timeout', 'dns', 'tls', 'denied'].map(value => ({ value, label: tr(value === 'success' ? 'success' : `${value}Option`) })))
const label = (kind: EgressCheck['kind']) => tr(kind === 'destination' ? 'destinationKind' : kind)
const busy = computed(() => task.state.value === 'running'); const localBusy = computed(() => localTask.state.value === 'running')
const missing = computed(() => !route.value || !revision.value || !terminal.value || (!!props.subscriptionId && !workspace.subscriptions.some(s => s.id === props.subscriptionId && s.routeIds.includes(props.routeId))) || (!!props.entryId && !entries.value.some(e => e.id === props.entryId)) || (!!entryId.value && !entries.value.some(e => e.id === entryId.value)) || (!!revision.value.policyId && (!policy.value || policy.value.terminalNodeId !== terminal.value?.id)))
const errors = computed(() => {
 const result: string[] = []
 if (scope.value === 'route' && policy.value && policy.value.revision !== revision.value?.policyRevision) result.push(tr('revisionUnavailable'))
 if (!enabled.value.length) result.push(tr('selectItems'))
 if (scope.value === 'target_only' && !target.value) result.push(tr('targetMissing'))
 const validUrl = (value: string) => { try { const url = new URL(value); return url.protocol === 'https:' && !!url.hostname && !url.username && !url.password && !url.hash } catch { return false } }
 if ((enabled.value.includes('destination') && !validUrl(destination.value)) || (enabled.value.includes('egress_ip') && !validUrl(echo.value))) result.push(tr('invalidUrl'))
 if (enabled.value.includes('dns_path') && (!/^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/i.test(domain.value) || domain.value.length > 253 || domain.value.includes('..'))) result.push(tr('invalidDomain'))
 return result
})
const resultQuery = ref(''); const resultStatus = ref('all')
const results = computed(() => workspace.checks.filter(check => check.routeId === props.routeId && check.scope === scope.value && (scope.value !== 'target_only' || check.targetId === targetId.value) && check.subscriptionId === effectiveSubscription.value && check.entryId === (entryId.value ?? undefined) && (resultStatus.value === 'all' || check.status === resultStatus.value) && `${check.name} ${check.testUrl} ${check.value ?? ''}`.toLowerCase().includes(resultQuery.value.trim().toLowerCase())).slice().sort((a,b) => b.observedAt.localeCompare(a.observedAt)))
function stale(check: typeof workspace.checks[number]) {
 const checkedTarget = workspace.targets.find(item => item.id === (check.selectedTargetId ?? check.targetId))
 return Date.parse(check.expiresAt) <= Date.now() || check.revision !== revision.value?.revision || check.policyId !== revision.value?.policyId || (!!policy.value && (policy.value.revision !== revision.value?.policyRevision || Date.parse(policy.value.updatedAt) > Date.parse(check.observedAt))) || (!!checkedTarget && Date.parse(checkedTarget.updatedAt) > Date.parse(check.observedAt)) || (!!(check.selectedTargetId ?? check.targetId) && !checkedTarget)
}
function cancel() { task.cancel(); localTask.cancel() }
function close() { cancel(); emit('update:show', false); emit('cancel') }
function initialize() { cancel(); resultQuery.value = ''; resultStatus.value = 'all'; version.value = route.value?.published ? 'published' : 'draft'; scope.value = props.scope ?? 'route'; targetId.value = props.targetId ?? null; entryId.value = props.entryId ?? null; outcome.value = 'success' }
watch(() => [props.show, props.routeId, props.subscriptionId, props.entryId, props.targetId, props.scope] as const, () => { if (props.show) initialize(); else cancel() }, { immediate: true })
watch(resetEpoch, initialize)
watch([version, scope, targetId, entryId, destination, echo, domain, outcome, enabled], cancel, { deep: true })
watch(() => JSON.stringify([route.value, policy.value, targets.value]), cancel)
function toggle(kind: EgressCheck['kind'], checked: boolean) { enabled.value = checked ? [...enabled.value, kind] : enabled.value.filter(item => item !== kind) }
async function run() {
 if (busy.value || missing.value || errors.value.length || !revision.value) return
 const contextRevision = revision.value; const contextPolicy = policy.value
 const simulation = workspace.scenario === 'offline' || terminal.value?.control === 'offline' ? 'timeout' : workspace.scenario === 'error' ? 'dns' : outcome.value
 if (!await task.run('success', 750)) return
 if (!props.show || missing.value) return
 const now = new Date(); const observedAt = now.toISOString(); const failed = simulation !== 'success'
 const ids: string[] = []
 for (const kind of kinds) {
  const attempted = enabled.value.includes(kind)
  let resolutionIp = kind === 'egress_ip' ? '198.51.100.42' : kind === 'dns_path' ? '192.0.2.53' : '203.0.113.42'
  if (kind === 'destination' && attempted) { const hostname = new URL(destination.value).hostname.replace(/^\[|\]$/g, ''); const suppliedIp = parseNetwork(hostname); if (suppliedIp) resolutionIp = hostname }
  const ip = parseNetwork(resolutionIp)!
  const matched = contextPolicy?.rules.find(rule => { if (rule.destination === 'MATCH') return true; const range = parseNetwork(rule.destination); return range?.version === ip.version && range.start <= ip.start && range.end >= ip.start })
  const selected = scope.value === 'target_only' ? target.value?.id : matched?.targetId
  const chosenTarget = targets.value.find(item => item.id === selected)
  const targetUnavailable = !!selected && selected !== 'REJECT' && (!chosenTarget || chosenTarget.tcp === 'failed')
  const ipv6Unsupported = kind === 'destination' && ip.version === 6

  const deniedByRule = scope.value === 'route' && selected === 'REJECT' && kind !== 'client_coverage'
  const status: EgressCheck['status'] = !attempted ? 'not_checked' : kind === 'client_coverage' || ipv6Unsupported ? 'not_supported' : failed || deniedByRule || targetUnavailable ? 'failed' : workspace.scenario === 'partial' && kind === 'egress_ip' ? 'not_checked' : 'observed'
  const check: EgressCheck = {
   id: newId('check'), name: label(kind), updatedAt: observedAt, routeId: props.routeId, kind, status, scope: scope.value,
   targetId: scope.value === 'target_only' ? targetId.value ?? undefined : undefined,
   subscriptionId: effectiveSubscription.value, entryId: entryId.value ?? undefined, policyId: contextRevision.policyId,
   testUrl: kind === 'destination' ? destination.value : kind === 'egress_ip' ? echo.value : kind === 'dns_path' ? domain.value : 'offline://client-coverage',
   observedAt, expiresAt: new Date(now.getTime() + (workspace.scenario === 'stale' ? -1000 : (status === 'failed' ? (workspace.settings.failureTtlMinutes ?? 5) * 60000 : (workspace.settings.measurementTtlHours ?? 96) * 3600000))).toISOString(), revision: contextRevision.revision,
   value: status === 'not_checked' ? null : status === 'not_supported' ? tr(ipv6Unsupported ? 'ipv6Hint' : 'coverageValue') : status === 'failed' ? tr(targetUnavailable ? 'targetMissing' : deniedByRule ? 'reject' : simulation === 'dns' ? 'dnsFailure' : simulation === 'tls' ? 'tlsFailure' : simulation === 'denied' ? 'denied' : 'timeout') : tr(kind === 'destination' ? 'destinationValue' : kind === 'egress_ip' ? 'echoValue' : 'dnsValue'),
   matchedRuleId: scope.value === 'route' && attempted ? matched?.id : undefined, selectedTargetId: selected && selected !== 'REJECT' ? selected : undefined,
   ruleResolutionIp: kind === 'destination' && attempted && status === 'observed' ? resolutionIp : undefined,
  }
  commands.upsert('checks', check); ids.push(check.id)
 }
 emit('completed', ids)
}
async function applyLocal() { if (!props.show || missing.value || localBusy.value) return; if (await localTask.run()) message.info(tr('localDone')) }
function reconnect() { dialog.warning({ title: tr('reconnectTitle'), content: tr('reconnectBody'), positiveText: tr('reconnect'), negativeText: tr('cancel'), onPositiveClick: applyLocal }) }
</script>
<template>
 <NDrawer :show="show" :width="720" style="max-width:100vw" @update:show="value => { if (!value) close() }">
  <NDrawerContent :title="tr('check')" closable><div class="content">
   <NAlert type="info" :show-icon="false">{{ tr('demo') }}</NAlert>
   <NAlert v-if="missing" type="error">{{ tr('missing') }}</NAlert>
   <template v-else>
    <p><RouterLink :to="entityLocation('route', routeId)">{{ route?.name }}</RouterLink> · <RouterLink v-if="terminal" :to="entityLocation('node', terminal.id)">{{ terminal.name }}</RouterLink> · <RouterLink v-if="effectiveSubscription" :to="entityLocation('subscription', effectiveSubscription)">{{ workspace.subscriptions.find(item => item.id === effectiveSubscription)?.name }}</RouterLink></p>
    <NAlert v-if="!route?.published" type="warning">{{ tr('noPublished') }}</NAlert>
    <div class="grid"><label>{{ tr('revision') }}<NSelect filterable v-model:value="version" :disabled="busy" :options="[{ value: 'published', label: `${tr('published')} · r${route?.published?.revision ?? '—'}`, disabled: !route?.published }, { value: 'draft', label: `${tr('draft')} · r${route?.draft.revision}` }]" /></label><label>{{ tr('scope') }}<NSelect filterable v-model:value="scope" :disabled="busy" :options="[{ label: tr('routeScope'), value: 'route' }, { label: tr('targetOnly'), value: 'target_only' }]" /></label></div>
    <p>{{ policy?.name ?? tr('nodeDefault') }} · {{ tr('revision') }}: {{ revision?.policyRevision ?? '—' }}</p>
    <label v-if="subscriptionId || entryId">{{ tr('entry') }}<NSelect filterable v-model:value="entryId" :disabled="busy || !!props.entryId" :clearable="!props.entryId" :options="entries.map(item => ({ value: item.id, label: item.name }))" /></label>
    <template v-if="scope === 'target_only'"><NAlert type="warning" :title="tr('targetOnly')">{{ tr('targetOnlyHint') }}</NAlert><label>{{ tr('target') }}<NSelect filterable v-model:value="targetId" :disabled="busy" :options="targets.map(item => ({ label: item.name, value: item.id }))" /></label></template>
    <section class="card"><h3>{{ tr('checkItems') }}</h3><NCheckbox v-for="kind in kinds" :key="kind" :checked="enabled.includes(kind)" :disabled="busy" @update:checked="value => toggle(kind, value)">{{ label(kind) }}</NCheckbox>
     <label v-if="enabled.includes('destination')">{{ tr('testUrl') }}<NInput v-model:value="destination" :disabled="busy" /></label>
     <label v-if="enabled.includes('egress_ip')">{{ tr('echoUrl') }}<NInput v-model:value="echo" :disabled="busy" /></label>
     <label v-if="enabled.includes('dns_path')">{{ tr('dnsDomain') }}<NInput v-model:value="domain" :disabled="busy" /></label>
     <label>{{ tr('failureMode') }}<NSelect filterable v-model:value="outcome" :disabled="busy" :options="outcomeOptions" /></label>
    </section>
    <NAlert v-if="errors.length" type="error"><ul><li v-for="error in errors" :key="error">{{ error }}</li></ul></NAlert>
    <div class="actions"><NButton type="primary" :loading="busy" :disabled="busy || errors.length > 0" @click="run">{{ busy ? tr('running') : tr('run') }}</NButton><NButton v-if="busy" @click="task.cancel">{{ tr('cancelRun') }}</NButton></div>
    <h3>{{ tr('result') }}</h3><div class="grid"><label>{{ tr('filterResults') }}<NInput v-model:value="resultQuery" clearable /></label><label>{{ tr('filterStatus') }}<NSelect v-model:value="resultStatus" :options="['all', 'observed', 'failed', 'not_supported', 'not_checked'].map(value => ({ value, label: tr(value) }))" /></label></div><p>{{ tr('freshness') }}</p><NAlert v-if="!results.length" type="default">{{ tr('noResults') }}</NAlert>
    <article v-for="check in results" :key="check.id" class="card">
     <h4>{{ label(check.kind) }} · {{ tr(check.status) }} <span v-if="stale(check)">· {{ tr('stale') }}</span></h4>
     <p>{{ tr('scope') }}: {{ check.scope === 'target_only' ? tr('targetOnly') : tr('routeScope') }} · {{ tr('routeRevision') }}: {{ check.revision }} · {{ tr('policyIdentity') }}: {{ check.policyId ?? tr('nodeDefault') }}</p>
     <p>{{ tr('testUrl') }}: {{ check.testUrl }}</p><p>{{ tr(check.status === 'not_checked' ? 'recordedAt' : 'checkedAt') }}: {{ format.date(check.observedAt) }} · {{ tr('expires') }}: {{ format.date(check.expiresAt) }}</p>
     <p>{{ tr('observation') }}: {{ check.value ?? tr('notChecked') }}</p><p v-if="check.kind === 'destination'">{{ tr('ruleIp') }}: {{ check.ruleResolutionIp ?? tr('notChecked') }}</p>
     <p>{{ tr('matched') }}: {{ check.matchedRuleId ?? tr('notChecked') }} · {{ tr('selected') }}: {{ workspace.targets.find(item => item.id === check.selectedTargetId)?.name ?? tr('notChecked') }}</p>
    </article>
    <section class="card"><h3>{{ tr('localClient') }}</h3><p>{{ tr('pairHint') }}</p><RouterLink :to="pageLocation('settings', {}, { section: 'client' })">{{ tr('settings') }}</RouterLink>
     <div v-if="localClientPaired" class="actions"><NButton :disabled="localBusy" :loading="localBusy" @click="applyLocal">{{ tr('apply') }}</NButton><NButton :disabled="localBusy" @click="reconnect">{{ tr('reconnect') }}</NButton></div>
    </section>
   </template>
  </div><template #footer><NButton @click="close">{{ tr('close') }}</NButton></template></NDrawerContent>
 </NDrawer>
</template>
<style scoped>
.content { display:grid; gap:16px; } .grid { display:grid; grid-template-columns:1fr 1fr; gap:12px; } label { display:grid; gap:6px; } .card { display:grid; gap:12px; padding:16px; border:1px solid var(--border-color,#dce5df); border-radius:10px; min-width:0; } h3,h4,p { margin:0; overflow-wrap:anywhere; } .actions { display:flex; gap:10px; flex-wrap:wrap; } @media(max-width:600px) { .grid { grid-template-columns:1fr; } button { min-height:44px; } }
</style>
