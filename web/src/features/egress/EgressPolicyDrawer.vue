<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { NAlert, NButton, NCheckbox, NDrawer, NDrawerContent, NInput, NInputNumber, NSelect, NSwitch, useDialog, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { RouterLink, onBeforeRouteLeave } from 'vue-router'
import { entityLocation, pageLocation } from '../../app/router'
import { useWorkspace, resetEpoch } from '../../demo/workspace'
import type { EgressPolicy, EgressTarget } from '../../domain'
import type { EgressPolicyDrawerProps, EgressPolicyDrawerEmits } from './types'
import { newId, overlaps, parseNetwork, targetIssue } from './logic'
const props = defineProps<EgressPolicyDrawerProps>(); const emit = defineEmits<EgressPolicyDrawerEmits>()
const { t } = useI18n(); const tr = (key: string) => t(`features.egress.${key}`)
const { workspace, commands, sessionSecrets } = useWorkspace(); const dialog = useDialog(); const message = useMessage()
const node = computed(() => workspace.nodes.find(n => n.id === props.terminalNodeId))
const source = computed(() => workspace.policies.find(p => p.id === props.policyId))
const route = computed(() => workspace.routes.find(r => r.id === props.routeId))
const missing = computed(() => !node.value || (!!props.policyId && !source.value) || (!!props.routeId && !route.value) || (!!source.value && source.value.terminalNodeId !== props.terminalNodeId))
const custom = ref(false); const name = ref(''); const noResolve = ref(false); const ipv6 = ref(false)
const targets = ref<EgressTarget[]>([]); const rules = ref<EgressPolicy['rules']>([]); const fallback = ref('REJECT')
const selectedPolicy = ref<string | null>(null); const saveMode = ref('copy'); const baseline = ref(''); const malformed = ref(false)
const drawerDraftId = computed(() => `egress-${props.draftId ?? props.routeId ?? 'new'}-${props.terminalNodeId}`)
const secretPrefix = newId('egress-auth'); const secretKeys = new Set<string>(); const secretCount = ref(0)
function secret(id: string, field: string) { return sessionSecrets.get(`${secretPrefix}:${id}:${field}`) ?? '' }
function setSecret(id: string, field: string, value: string) { const key = `${secretPrefix}:${id}:${field}`; secretKeys.add(key); secretCount.value = secretKeys.size; sessionSecrets.set(key, value) }
function clearSecrets() { for (const key of secretKeys) sessionSecrets.delete(key); secretKeys.clear(); secretCount.value = 0 }
const snapshot = () => JSON.stringify({ custom: custom.value, name: name.value, noResolve: noResolve.value, ipv6: ipv6.value, targets: targets.value, rules: rules.value, fallback: fallback.value, selectedPolicy: selectedPolicy.value, saveMode: saveMode.value })
const dirty = computed(() => snapshot() !== baseline.value || secretCount.value > 0)
const policyOptions = computed(() => workspace.policies.filter(p => p.terminalNodeId === props.terminalNodeId).map(p => ({ label: `${p.name} · r${p.revision}`, value: p.id })))
const loadedPolicy = computed(() => workspace.policies.find(p => p.id === selectedPolicy.value))
const references = computed(() => workspace.routes.filter(r => r.draft.policyId === selectedPolicy.value || r.published?.policyId === selectedPolicy.value))
const targetOptions = computed(() => [{ label: tr('reject'), value: 'REJECT' }, ...targets.value.map(target => ({ label: `${target.name} · ${target.type}`, value: target.id }))])
const typeOptions = computed(() => ['node_default', 'interface', 'http', 'socks5'].map(value => ({ value, label: value === 'node_default' ? tr('nodeDefault') : value === 'interface' ? tr('interface') : value.toUpperCase() })))
function load(id: string | null) {
 clearSecrets(); selectedPolicy.value = id; saveMode.value = 'copy'
 const policy = workspace.policies.find(p => p.id === id)
 custom.value = !!policy; name.value = policy?.name ?? `${node.value?.name ?? ''} Egress`; noResolve.value = policy?.noResolve ?? false; ipv6.value = policy?.ipv6 ?? false
 targets.value = JSON.parse(JSON.stringify(workspace.targets.filter(target => target.nodeId === props.terminalNodeId)))
 const allRules: EgressPolicy['rules'] = JSON.parse(JSON.stringify(policy?.rules ?? []))
 malformed.value = !!policy && (allRules.filter(r => r.destination === 'MATCH').length !== 1 || allRules.at(-1)?.destination !== 'MATCH')
 rules.value = allRules.filter(r => r.destination !== 'MATCH'); fallback.value = allRules.find(r => r.destination === 'MATCH')?.targetId ?? 'REJECT'
 baseline.value = snapshot()
}
function selectPolicy(id: string | null) {
 if (!dirty.value) { load(id); return }
 dialog.warning({ title: tr('dirty'), content: tr('dirtyBody'), positiveText: tr('discard'), negativeText: tr('stay'), onPositiveClick: () => load(id) })
}
function restore() {
 load(props.policyId ?? null)
 if (props.readonly) return
 const saved = workspace.drafts.find(d => d.id === drawerDraftId.value)?.data?.egress
 if (!saved) return
 const data = JSON.parse(JSON.stringify(saved))
 custom.value = data.custom; name.value = data.name; noResolve.value = data.noResolve; ipv6.value = data.ipv6; targets.value = data.targets; rules.value = data.rules; fallback.value = data.fallback; selectedPolicy.value = data.selectedPolicy
 baseline.value = snapshot()
}
function saveDrawerDraft() {
 const data = JSON.parse(snapshot())
 commands.saveDraft({ id: drawerDraftId.value, feature: 'egress', objectId: props.routeId, name: name.value, nodeIds: [props.terminalNodeId], routeIds: props.routeId ? [props.routeId] : [], updatedAt: new Date().toISOString(), data: { egress: data } })
 clearSecrets(); baseline.value = snapshot(); message.success(tr('draftSaved'))
}
watch(() => [props.show, props.terminalNodeId, props.policyId, props.routeId, props.draftId] as const, () => { if (props.show) restore(); else clearSecrets() }, { immediate: true })
watch(resetEpoch, () => { if (props.show) load(props.policyId ?? null); else clearSecrets() })
const unload = (event: BeforeUnloadEvent) => { if (props.show && !props.readonly && dirty.value) { event.preventDefault(); event.returnValue = '' } }
window.addEventListener('beforeunload', unload)
onBeforeUnmount(() => { clearSecrets(); window.removeEventListener('beforeunload', unload) })
onBeforeRouteLeave(() => {
 if (!props.show || props.readonly || !dirty.value) return true
 return new Promise<boolean>(resolve => {
  let settled = false
  const finish = (leave: boolean) => { if (!settled) { settled = true; if (leave) close(true); resolve(leave) } }
  dialog.warning({ title: tr('dirty'), content: tr('dirtyBody'), positiveText: tr('discard'), negativeText: tr('stay'), onPositiveClick: () => finish(true), onNegativeClick: () => finish(false), onClose: () => finish(false), onMaskClick: () => finish(false), onAfterLeave: () => finish(false) })
 })
})
function close(force = false) {
 if (!force && !props.readonly && dirty.value) { dialog.warning({ title: tr('dirty'), content: tr('dirtyBody'), positiveText: tr('discard'), negativeText: tr('stay'), onPositiveClick: () => close(true) }); return }
 clearSecrets(); emit('update:show', false); emit('cancel')
}
function addTarget() { targets.value.push({ id: newId('target'), name: '', nodeId: props.terminalNodeId, type: 'node_default', tcp: 'not_checked', udp: 'not_checked', updatedAt: new Date().toISOString() }) }
function changeType(target: EgressTarget, value: EgressTarget['type']) { target.type = value; target.tcp = 'not_checked'; target.udp = 'not_checked'; delete target.host; delete target.port; delete target.interfaceName }
function addRule() { rules.value.push({ id: newId('rule'), destination: '', targetId: 'REJECT' }) }
function move(index: number, offset: number) { const item = rules.value[index]; if (!item || index + offset < 0 || index + offset >= rules.value.length) return; rules.value.splice(index, 1); rules.value.splice(index + offset, 0, item) }
function normalize() { for (const rule of rules.value) { const parsed = parseNetwork(rule.destination); if (parsed) rule.destination = parsed.text } }
const warnings = computed(() => {
 const result: string[] = []
 rules.value.forEach((rule, i) => { const a = parseNetwork(rule.destination); if (a) rules.value.slice(i + 1).forEach((next, j) => { const b = parseNetwork(next.destination); if (b && overlaps(a, b)) result.push(t('features.egress.overlap', { first: i + 1, second: i + j + 2 })) }) })
 return result
})
const errors = computed(() => {
 if (!custom.value) return []
 const result: string[] = []; if (!name.value.trim()) result.push(tr('nameRequired'))
 if (ipv6.value) result.push(tr('ipv6Hint'))
 for (const target of targets.value) { const issue = targetIssue(target, workspace.listeners, [node.value?.address ?? '', node.value?.controlAddress ?? '']); if (issue) result.push(`${target.name || tr('targets')}: ${tr(issue)}`) }
 rules.value.forEach((rule, i) => { const net = parseNetwork(rule.destination); if (!net) result.push(t('features.egress.invalidIp', { row: i + 1 })); else if (net.version === 6) result.push(t('features.egress.ipv6Invalid', { row: i + 1 })); if (rule.targetId !== 'REJECT' && !targets.value.some(target => target.id === rule.targetId)) result.push(t('features.egress.invalidTarget', { row: i + 1 })) })
 if (fallback.value !== 'REJECT' && !targets.value.some(target => target.id === fallback.value)) result.push(t('features.egress.invalidTarget', { row: 'MATCH' }))
 return result
})
function commit() {
 if (!props.show || missing.value || props.readonly || errors.value.length) return
 const now = new Date().toISOString(); const original = loadedPolicy.value
 const policy: EgressPolicy = { id: saveMode.value === 'update' && original ? original.id : newId('policy'), name: name.value.trim() || `${node.value?.name ?? ''} Egress`, terminalNodeId: props.terminalNodeId, revision: saveMode.value === 'update' && original ? original.revision + 1 : 1, rules: [], updatedAt: now, noResolve: custom.value && noResolve.value, ipv6: false }
 const usedTargets = new Set(custom.value ? [...rules.value.map(r => r.targetId), fallback.value] : [])
 const mapping = new Map<string, string>()
 if (!custom.value) { const target: EgressTarget = { id: newId('target'), nodeId: props.terminalNodeId, name: tr('nodeDefault'), type: 'node_default', tcp: 'not_checked', udp: 'not_checked', updatedAt: now }; commands.upsert('targets', target); policy.rules = [{ id: newId('rule'), destination: 'MATCH', targetId: target.id }] }
 else {
  for (const target of targets.value.filter(target => usedTargets.has(target.id))) {
   const previous = workspace.targets.find(item => item.id === target.id)
   if (previous && JSON.stringify(previous) === JSON.stringify(target)) { mapping.set(target.id, target.id); continue }
   const copy: EgressTarget = { ...target, id: newId('target'), tcp: 'not_checked', udp: 'not_checked', updatedAt: now }; commands.upsert('targets', copy); mapping.set(target.id, copy.id)
  }
  policy.rules = [...rules.value.map(rule => ({ ...rule, destination: parseNetwork(rule.destination)!.text, targetId: mapping.get(rule.targetId) ?? rule.targetId })), { id: newId('rule'), destination: 'MATCH', targetId: mapping.get(fallback.value) ?? fallback.value }]
 }
 commands.remove('drafts', drawerDraftId.value)
 clearSecrets(); baseline.value = snapshot(); emit('save', policy); emit('update:show', false); message.success(tr('saved'))
}
function save() {
 if (saveMode.value === 'update' && loadedPolicy.value) { dialog.warning({ title: tr('confirm'), content: `${tr('confirmBody')} ${references.value.map(r => r.name).join(', ')}`, positiveText: tr('save'), negativeText: tr('cancel'), onPositiveClick: commit }); return }
 commit()
}
</script>
<template>
 <NDrawer :show="show" :width="720" style="max-width:100vw" @update:show="value => { if (!value) close() }">
  <NDrawerContent :title="tr('policy')" closable>
   <div class="content">
    <NAlert type="info" :show-icon="false">{{ tr('demo') }}</NAlert>
    <NAlert v-if="missing" type="error">{{ tr('missing') }}</NAlert>
    <template v-else>
     <p><strong>{{ tr('applies') }}:</strong> <RouterLink :to="entityLocation('node', terminalNodeId)">{{ node?.name }}</RouterLink></p>
     <p>{{ tr('terminal') }}</p><NAlert v-if="readonly" type="warning">{{ tr('readonly') }}</NAlert>
     <fieldset :disabled="readonly" :inert="readonly ? true : undefined"><label>{{ tr('reuse') }}<NSelect filterable :value="selectedPolicy" :options="policyOptions" clearable :placeholder="tr('newPolicy')" :disabled="readonly" @update:value="selectPolicy" /></label>
      <label class="inline"><NSwitch v-model:value="custom" :disabled="readonly" />{{ custom ? tr('custom') : tr('nodeDefault') }}</label>
      <template v-if="custom">
       <label>{{ tr('name') }}<NInput v-model:value="name" :maxlength="120" /></label>
       <NAlert v-if="malformed" type="warning">{{ tr('policyInvalid') }}</NAlert>
       <h3>{{ tr('targets') }}</h3><p>{{ tr('udp') }}</p>
       <p v-if="!targets.length">{{ tr('noTargets') }}</p>
       <section v-for="target in targets" :key="target.id" class="card">
        <div class="grid"><label>{{ tr('targetNameLabel') }}<NInput v-model:value="target.name" :maxlength="120" /></label><label>{{ tr('type') }}<NSelect filterable :value="target.type" :options="typeOptions" @update:value="value => changeType(target, value)" /></label></div>
        <template v-if="target.type === 'interface'"><label>{{ tr('interface') }}<NInput v-model:value="target.interfaceName" placeholder="eth0" /></label><p>{{ tr('interfaceHint') }}</p></template>
        <template v-if="target.type === 'http' || target.type === 'socks5'">
         <div class="grid"><label>{{ tr('host') }}<NInput v-model:value="target.host" placeholder="proxy.example.invalid" /></label><label>{{ tr('port') }}<NInputNumber :value="target.port ?? null" :min="1" :max="65535" @update:value="value => { target.port = value ?? undefined }" /></label></div>
         <p>{{ tr('proxyHint') }}</p><details><summary>{{ tr('auth') }}</summary><div class="grid"><label>{{ tr('username') }}<NInput :value="secret(target.id, 'username')" autocomplete="off" @update:value="value => setSecret(target.id, 'username', value)" /></label><label>{{ tr('password') }}<NInput type="password" :value="secret(target.id, 'password')" autocomplete="new-password" @update:value="value => setSecret(target.id, 'password', value)" /></label></div><p>{{ tr('authHint') }}</p></details>
        </template>
        <small>TCP: {{ target.tcp }} · UDP: {{ target.type === 'http' || target.udp !== 'verified' ? tr('reject') : target.udp }}</small>
        <NButton size="small" @click="targets.splice(targets.indexOf(target), 1)">{{ tr('remove') }}</NButton>
       </section>
       <NButton @click="addTarget">{{ tr('addTarget') }}</NButton>
       <h3>{{ tr('rules') }}</h3><p>{{ tr('firstMatch') }}</p><p v-if="!rules.length">{{ tr('noRules') }}</p>
       <section v-for="(rule, index) in rules" :key="rule.id" class="card">
        <div class="grid"><label>{{ index + 1 }}. {{ tr('destination') }}<NInput v-model:value="rule.destination" placeholder="203.0.113.0/24" /></label><label>{{ tr('target') }}<NSelect filterable v-model:value="rule.targetId" :options="targetOptions" /></label></div>
        <div class="actions"><NButton :disabled="index === 0 || readonly" @click="move(index, -1)">{{ tr('up') }}</NButton><NButton :disabled="index === rules.length - 1 || readonly" @click="move(index, 1)">{{ tr('down') }}</NButton><NButton @click="rules.splice(index, 1)">{{ tr('remove') }}</NButton></div>
       </section>
       <div class="actions"><NButton @click="addRule">{{ tr('addRule') }}</NButton><NButton @click="normalize">{{ tr('normalize') }}</NButton><NButton @click="errors.length ? message.error(errors[0] ?? '') : message.success(tr('valid'))">{{ tr('validate') }}</NButton></div>
       <label>{{ tr('unmatched') }}<NSelect filterable v-model:value="fallback" :options="targetOptions" /></label>
       <NCheckbox v-model:checked="noResolve">{{ tr('noResolve') }}</NCheckbox>
       <label class="inline"><NSwitch v-model:value="ipv6" :disabled="!ipv6 || readonly" />{{ tr('ipv6') }}</label><p>{{ tr('ipv6Hint') }}</p>
       <NAlert v-for="warning in warnings" :key="warning" type="warning">{{ warning }}</NAlert>
       <template v-if="loadedPolicy"><h3>{{ tr('shared') }}</h3><p v-if="!references.length">{{ tr('notChecked') }}</p><p v-for="reference in references" :key="reference.id"><RouterLink :to="entityLocation('route', reference.id)">{{ reference.name }}</RouterLink> · Draft r{{ reference.draft.policyRevision ?? '—' }} · Published r{{ reference.published?.policyRevision ?? '—' }}</p><label>{{ tr('saveMode') }}<NSelect filterable v-model:value="saveMode" :options="[{ label: tr('copy'), value: 'copy' }, { label: tr('update'), value: 'update', disabled: true }]" /></label><p>{{ tr('updateHint') }}</p></template>
      </template>
     </fieldset>
     <section class="card"><h3>{{ tr('dns') }} · r{{ node?.targetDns.revision }}</h3><p>{{ node?.targetDns.upstreams.join(', ') || tr('notChecked') }}</p><p>{{ tr('dnsOutbound') }}: {{ node?.dnsRouting?.outbound ?? tr('nodeDefault') }}</p><p>{{ tr('bootstrap') }}: {{ node?.dnsRouting?.bootstrap.join(', ') || tr('notChecked') }}</p><p>{{ tr('dnsHint') }}</p><RouterLink :to="pageLocation('node-detail', { nodeId: terminalNodeId }, { tab: 'dns' })">{{ tr('dnsSettings') }}</RouterLink></section>
     <NAlert type="info">{{ tr('draftGap') }}</NAlert><NAlert v-if="errors.length" type="error"><ul><li v-for="error in errors" :key="error">{{ error }}</li></ul></NAlert>
    </template>
   </div>
   <template #footer><div class="actions"><NButton @click="close()">{{ readonly ? tr('close') : tr('cancel') }}</NButton><NButton v-if="!readonly" :disabled="missing" @click="saveDrawerDraft">{{ tr('saveDraft') }}</NButton><NButton v-if="!readonly" type="primary" :disabled="missing || errors.length > 0" @click="save">{{ tr('save') }}</NButton></div></template>
  </NDrawerContent>
 </NDrawer>
</template>
<style scoped>
.content { display:grid; gap:16px; } fieldset { border:0; padding:0; margin:0; display:grid; gap:16px; min-width:0; } label { display:grid; gap:6px; } .inline,.actions { display:flex; gap:10px; align-items:center; flex-wrap:wrap; } .grid { display:grid; grid-template-columns:1fr 1fr; gap:12px; } .card { padding:16px; border:1px solid var(--border-color, #dce5df); border-radius:10px; display:grid; gap:12px; min-width:0; } h3,p { margin:0; } p,small { overflow-wrap:anywhere; } button { min-height:36px; } @media(max-width:600px) { .grid { grid-template-columns:1fr; } button { min-height:44px; } }
</style>
