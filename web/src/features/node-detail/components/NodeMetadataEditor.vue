<script setup lang="ts">
import { computed, ref } from 'vue'
import { onBeforeRouteUpdate } from 'vue-router'
import { NDrawer, NDrawerContent, NForm, NFormItem, NInput, NSelect, NButton, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useDraft } from '../../../composables/useDraft'
import { cloneNode, issue, validHost } from '../model'
import { useNodeContext } from '../useNodeContext'
import ValidationSummary from '../../../components/ValidationSummary.vue'
import type { Draft, Issue } from '../../../domain'
const props = defineProps<{ nodeId: string; show: boolean }>()
const emit = defineEmits<{ 'update:show': [value: boolean] }>()
const { node, routes, workspace, commands } = useNodeContext(props.nodeId)
const { t } = useI18n()
const message = useMessage()
const key = 'features.node-detail.'
const initial: Draft = { id: `node-detail-${props.nodeId}`, feature: 'node-detail', objectId: props.nodeId, name: node.value.name, nodeIds: [props.nodeId], routeIds: [], updatedAt: new Date().toISOString(),
  data: { node: { mode: node.value.mode, address: node.value.address ?? '', location: node.value.location, city: node.value.city ?? '', environment: node.value.environment ?? workspace.settings.environment ?? 'outside_mainland_china' } } }
const draft = useDraft(initial.id, initial)
const form = computed(() => draft.value.value.data?.node)
const issues = ref<Issue[]>([])
const environmentOptions = computed(() => ['mainland_china', 'outside_mainland_china'].map(value => ({ value, label: t(key + value) })))
function saveDraft() { draft.save(); message.success(t('common.saved')) }
function cancel() { draft.discard(); issues.value = []; emit('update:show', false) }
function close() {
  // Preserve unsaved values while the drawer is hidden; navigation still uses useDraft's guard.
  emit('update:show', false)
}
onBeforeRouteUpdate((to, from) => {
  if (to.params.nodeId !== from.params.nodeId && draft.dirty.value) { emit('update:show', true); message.warning(t(key + 'finishMetadata')); return false }
})
function save() {
  const data = form.value
  if (!data || node.value.removedAt) return
  issues.value = []
  if (!draft.value.value.name.trim()) issues.value.push(issue('NAME', 'node-name'))
  if (!data.location.trim()) issues.value.push(issue('LOCATION', 'node-location'))
  if (data.mode === 'direct' && !validHost(data.address.trim())) issues.value.push(issue('ADDRESS', 'node-address'))
  if (issues.value.length) return
  const next = cloneNode(node.value)
  const addressChanged = next.address !== data.address.trim() && next.mode === 'direct'
  next.name = draft.value.value.name.trim(); next.location = data.location.trim(); next.city = data.city?.trim(); next.environment = data.environment
  next.manualLocation = next.manualLocation || next.location !== node.value.location || next.city !== node.value.city
  if (next.mode === 'direct') next.address = data.address.trim()
  next.updatedAt = new Date().toISOString()
  if (addressChanged) {
    next.verification = 'not_checked'
    for (const original of workspace.measurements.filter(m => routes.value.some(r => r.id === m.routeId))) {
      commands.upsert('measurements', { ...original, expiresAt: next.updatedAt })
    }
  }
  commands.upsert('nodes', next)
  draft.save(); commands.remove('drafts', initial.id)
  message.success(t(key + 'metadataSaved')); emit('update:show', false)
}
function focus(field: string) { document.getElementById(field)?.focus() }
</script>
<template>
  <NDrawer :show="show" :width="560" @update:show="close">
    <NDrawerContent :title="t(key + 'edit')" closable>
      <p>{{ t(key + 'metadataHint') }}</p>
      <ValidationSummary :issues="issues" @focus="focus" />
      <NForm v-if="form" label-placement="top" @submit.prevent="save">
        <NFormItem :label="t(key + 'name')"><NInput v-model:value="draft.value.value.name" :maxlength="80" :input-props="{ id: 'node-name' }" /></NFormItem>
        <NFormItem :label="t(key + 'proxyAddress')"><NInput v-model:value="form.address" :disabled="form.mode === 'reverse'" :input-props="{ id: 'node-address' }" :maxlength="253" /></NFormItem>
        <NFormItem :label="t(key + 'location')"><NInput v-model:value="form.location" :input-props="{ id: 'node-location' }" :maxlength="80" /></NFormItem>
        <NFormItem :label="t(key + 'city')"><NInput v-model:value="form.city" :maxlength="80" /></NFormItem>
        <NFormItem :label="t(key + 'environment')"><NSelect v-model:value="form.environment" :options="environmentOptions" /></NFormItem>
        <div class="actions"><NButton :disabled="!!node.removedAt" type="primary" attr-type="submit">{{ t(key + 'saveMetadata') }}</NButton><NButton @click="saveDraft">{{ t('common.saveDraft') }}</NButton><NButton @click="cancel">{{ t('common.discard') }}</NButton></div>
      </NForm>
    </NDrawerContent>
  </NDrawer>
</template>
