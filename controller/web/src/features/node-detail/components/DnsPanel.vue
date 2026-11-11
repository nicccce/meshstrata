<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { NInput, NSelect, NButton, NAlert, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { entityLocation } from '../../../app/router'
import type { Issue } from '../../../domain'
import ValidationSummary from '../../../components/ValidationSummary.vue'
import TaskFeedback from '../../../components/TaskFeedback.vue'
import { useNodeContext, useNodeRelease } from '../useNodeContext'
import { cloneNode, cloneRoute, lines, validIp, validUpstream, issue } from '../model'
import { useEditorGuard } from '../useEditorGuard'
const props = defineProps<{ nodeId: string }>()
const { node, routes, workspace, commands } = useNodeContext(props.nodeId)
const { t } = useI18n(); const message = useMessage(); const key = 'features.node-detail.'
function current() { return { upstreams: (node.value.dnsDraft?.upstreams ?? node.value.targetDns.upstreams).join('\n'), outbound: node.value.dnsDraft?.outbound ?? node.value.dnsRouting?.outbound ?? 'direct', bootstrap: (node.value.dnsDraft?.bootstrap ?? node.value.dnsRouting?.bootstrap ?? []).join('\n') } }
const form = ref(current()); const baseline = ref(JSON.stringify(form.value))
const dirty = computed(() => JSON.stringify(form.value) !== baseline.value)
const reviewing = ref(false); const issues = ref<Issue[]>([]); const releaseId = ref<string>()
const task = useNodeRelease(props.nodeId)
const busy = computed(() => task.state.value === 'running')
const choices = computed(() => [{ value: 'direct', label: t(key + 'explicitDirect') }, ...workspace.targets.filter(target => target.nodeId === props.nodeId).map(target => ({ value: target.id, label: target.name }))])
const parsed = computed(() => ({ upstreams: lines(form.value.upstreams), outbound: form.value.outbound, bootstrap: lines(form.value.bootstrap) }))
const differsFromApplied = computed(() => JSON.stringify(parsed.value) !== JSON.stringify({ upstreams: [...node.value.targetDns.upstreams], outbound: node.value.dnsRouting?.outbound ?? 'direct', bootstrap: [...(node.value.dnsRouting?.bootstrap ?? [])] }))
watch(form, () => { reviewing.value = false }, { deep: true })
function validate() {
  const result: Issue[] = []
  if (!parsed.value.upstreams.length || parsed.value.upstreams.some(value => !validUpstream(value))) result.push(issue('DNS_UPSTREAM', 'dns-upstreams'))
  if (!choices.value.some(choice => choice.value === parsed.value.outbound)) result.push(issue('DNS_OUTBOUND', 'dns-outbound'))
  if (parsed.value.bootstrap.some(value => !validIp(value))) result.push(issue('DNS_BOOTSTRAP', 'dns-bootstrap'))
  const domainUpstream = parsed.value.upstreams.some(value => { if (validIp(value)) return false; try { const hostname = new URL(value).hostname.replace(/^\[|\]$/g, ''); return !validIp(hostname) } catch { return false } })
  if (domainUpstream && !parsed.value.bootstrap.length) result.push(issue('BOOTSTRAP_REQUIRED', 'dns-bootstrap'))
  if (parsed.value.upstreams.length > 16 || parsed.value.bootstrap.length > 16) result.push(issue('DNS_LIMIT'))
  issues.value = result
  return !result.length
}
function saveDraft() {
  if (!validate() || node.value.removedAt) return false
  const next = cloneNode(node.value); next.dnsDraft = { ...parsed.value }; next.updatedAt = new Date().toISOString(); commands.upsert('nodes', next)
  baseline.value = JSON.stringify(form.value); message.success(t(key + 'dnsDraftSaved')); return true
}
function discard() { form.value = current(); baseline.value = JSON.stringify(form.value); issues.value = []; reviewing.value = false }
function useApplied() { form.value = { upstreams: node.value.targetDns.upstreams.join('\n'), outbound: node.value.dnsRouting?.outbound ?? 'direct', bootstrap: (node.value.dnsRouting?.bootstrap ?? []).join('\n') }; reviewing.value = false }
function dropDraft() {
  const next = cloneNode(node.value); delete next.dnsDraft; commands.upsert('nodes', next); discard()
  message.info(t(key + 'dnsDraftDiscarded'))
}
useEditorGuard(dirty, discard, saveDraft)
function review() { if (validate()) reviewing.value = true }
async function publish() {
  if (busy.value || !validate() || !differsFromApplied.value || node.value.removedAt) return
  const config = { upstreams: [...parsed.value.upstreams], outbound: parsed.value.outbound, bootstrap: [...parsed.value.bootstrap] }
  const revision = node.value.targetDns.revision
  saveDraft()
  const result = await task.publish(t(key + 'dnsRelease', { name: node.value.name }), revision + 1, () => {
    if (node.value.targetDns.revision !== revision) return false
    const now = new Date().toISOString(); const next = cloneNode(node.value)
    next.targetDns = { revision: revision + 1, upstreams: config.upstreams }; next.dnsRouting = { outbound: config.outbound, bootstrap: config.bootstrap }; delete next.dnsDraft
    next.appliedRevision = (next.appliedRevision ?? 0) + 1; next.updatedAt = now
    commands.upsert('nodes', next)
    for (const original of workspace.checks.filter(check => ['dns_path', 'destination', 'egress_ip'].includes(check.kind) && routes.value.some(route => route.id === check.routeId))) commands.upsert('checks', { ...original, expiresAt: now })
    for (const original of routes.value) { const edited = cloneRoute(original); edited.draft.validated = false; commands.upsert('routes', edited) }
    baseline.value = JSON.stringify(form.value); reviewing.value = false
  }, [t(key + 'validateDns'), t(key + 'applyDns'), t(key + 'verifyDns')])
  if (result) { releaseId.value = result.id; if (result.success) message.success(t(key + 'dnsApplied')) }
}
function focus(field: string) { document.getElementById(field)?.focus() }
</script>
<template>
  <div class="dns-layout">
    <div>
      <section class="panel">
        <h2>{{ t(key + 'appliedDns', { revision: node.targetDns.revision }) }}</h2>
        <p>{{ t(key + 'dnsHint') }}</p>
        <dl><dt>{{ t(key + 'upstreams') }}</dt><dd><div v-for="upstream in node.targetDns.upstreams" :key="upstream">{{ upstream }}</div></dd><dt>{{ t(key + 'outbound') }}</dt><dd>{{ node.dnsRouting?.outbound ?? t(key + 'explicitDirect') }}</dd><dt>{{ t(key + 'bootstrap') }}</dt><dd>{{ node.dnsRouting?.bootstrap?.join(', ') || t(key + 'notConfigured') }}</dd></dl>
      </section>
      <section class="panel">
        <h2>{{ t(key + 'dnsEditor') }}</h2>
        <NAlert v-if="node.dnsDraft" type="info">{{ t(key + 'dnsPending') }}</NAlert>
        <ValidationSummary :issues="issues" @focus="focus" />
        <form @submit.prevent="review">
          <fieldset :disabled="busy || !!node.removedAt">
            <label for="dns-upstreams">{{ t(key + 'upstreams') }}</label><NInput v-model:value="form.upstreams" type="textarea" :rows="4" :maxlength="4000" :input-props="{ id: 'dns-upstreams' }" />
            <p>{{ t(key + 'upstreamHelp') }}</p>
            <label id="dns-outbound-label">{{ t(key + 'outbound') }}</label><NSelect v-model:value="form.outbound" :options="choices" input-id="dns-outbound" aria-labelledby="dns-outbound-label" />
            <label for="dns-bootstrap">{{ t(key + 'bootstrap') }}</label><NInput v-model:value="form.bootstrap" type="textarea" :rows="2" :maxlength="1000" :input-props="{ id: 'dns-bootstrap' }" />
            <p>{{ t(key + 'bootstrapHelp') }}</p>
          </fieldset>
          <div class="actions"><NButton attr-type="submit" type="primary" :disabled="busy || !differsFromApplied || !!node.removedAt">{{ t(key + 'reviewDns') }}</NButton><NButton :disabled="busy || !!node.removedAt" @click="saveDraft">{{ t('common.saveDraft') }}</NButton><NButton :disabled="busy || !dirty" @click="discard">{{ t('common.discard') }}</NButton><NButton :disabled="busy || !!node.removedAt" @click="useApplied">{{ t(key + 'useApplied') }}</NButton><NButton v-if="node.dnsDraft" :disabled="busy || !!node.removedAt" @click="dropDraft">{{ t(key + 'dropDnsDraft') }}</NButton></div>
        </form>
        <section v-if="reviewing" class="review" aria-live="polite">
          <h3>{{ t(key + 'reviewDns') }}</h3><p>{{ t(key + 'dnsReviewHint', { count: routes.length, revision: node.targetDns.revision + 1 }) }}</p>
          <dl><dt>{{ t(key + 'upstreams') }}</dt><dd>{{ parsed.upstreams.join(', ') }}</dd><dt>{{ t(key + 'outbound') }}</dt><dd>{{ choices.find(option => option.value === parsed.outbound)?.label }}</dd><dt>{{ t(key + 'bootstrap') }}</dt><dd>{{ parsed.bootstrap.join(', ') || t(key + 'none') }}</dd></dl>
          <NAlert v-if="node.control !== 'online'" type="warning">{{ t(key + 'offlinePublish') }}</NAlert>
          <div class="actions"><NButton type="primary" :loading="busy" @click="publish">{{ t(key + 'simulateDnsRelease') }}</NButton><NButton :disabled="busy" @click="reviewing = false">{{ t(key + 'backToEdit') }}</NButton></div>
        </section>
        <TaskFeedback v-if="task.state.value !== 'idle'" :state="task.state.value" @cancel="task.cancel" />
        <div v-if="releaseId" class="actions"><RouterLink :to="entityLocation('release', releaseId)">{{ t(key + 'viewRelease') }}</RouterLink></div>
      </section>
    </div>
    <aside class="panel"><h2>{{ t(key + 'affectedRoutes') }}</h2><p>{{ t(key + 'dnsScope') }}</p><ul v-if="routes.length"><li v-for="route in routes" :key="route.id"><RouterLink :to="entityLocation('route', route.id)">{{ route.name }}</RouterLink></li></ul><p v-else>{{ t(key + 'noRoutes') }}</p></aside>
  </div>
</template>
<style scoped>
.dns-layout { display: grid; grid-template-columns: minmax(0,1fr) minmax(220px, 280px); gap: 24px; align-items: start; }.dns-layout > div { min-width: 0; }fieldset { border: 0; padding: 0; margin: 16px 0; min-width: 0; }label { margin-top: 20px; }dd { margin-bottom: 20px; }.review { background: #f1f7f3; padding: 20px; border-radius: 8px; margin-top: 24px; }li { margin: 12px 0; }@media(max-width:1050px){.dns-layout{grid-template-columns:1fr}}
</style>
