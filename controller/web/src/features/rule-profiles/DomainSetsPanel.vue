<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NCheckboxGroup, NCheckbox, NFormItem, NInput, NModal, NSelect, useDialog, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useWorkspace } from '../../demo/workspace'
import { entityLocation } from '../../app/router'
import { containsSensitiveInput, isDomain, newId, setReferences } from './rules'
const props = defineProps<{ selected: string[]; targets: string[]; readonly?: boolean }>()
const emit = defineEmits<{ 'update:selected': [ids: string[]] }>()
const { t } = useI18n(); const tr = (key: string) => t(`features.rule-profiles.${key}`)
const { workspace, commands } = useWorkspace(); const message = useMessage(); const dialog = useDialog()
const show = ref(false), editingId = ref(''), name = ref(''), identifier = ref(''), members = ref(''), group = ref('REJECT'), tried = ref(false)
const refs = computed(() => editingId.value ? setReferences(workspace, editingId.value) : { profiles: [], drafts: [] })
const domains = computed(() => members.value.split(/\r?\n/).map(v => v.trim().toLowerCase()).filter(Boolean))
const problems = computed(() => {
  const errors: string[] = []
  if (!name.value.trim() || name.value.trim().length > 100 || containsSensitiveInput(name.value)) errors.push('setName')
  if (!/^[A-Za-z][A-Za-z0-9_-]{0,79}$/.test(identifier.value) || workspace.domainSets.some(s => s.identifier === identifier.value && s.id !== editingId.value)) errors.push('identifier')
  if (!domains.value.length || domains.value.length > 1000 || domains.value.some(domain => !isDomain(domain)) || new Set(domains.value).size !== domains.value.length) errors.push('setMembers')
  if (!props.targets.includes(group.value)) errors.push('setGroup')
  return errors
})
function edit(id?: string) {
  const set = workspace.domainSets.find(item => item.id === id)
  editingId.value = set?.id ?? ''; name.value = set?.name ?? ''; identifier.value = set?.identifier ?? ''; members.value = set?.domains.join('\n') ?? ''; group.value = set?.group ?? 'REJECT'; tried.value = false; show.value = true
}
function save() {
  tried.value = true
  if (problems.value.length) return
  if (workspace.scenario === 'error') { message.error(tr('failed')); return }
  const original = workspace.domainSets.find(set => set.id === editingId.value)
  if (editingId.value && !original) { message.error(t('features.rule-profiles.errors.setMissing', { detail: editingId.value })); return }
  const id = original?.id ?? newId('domain-set')
  commands.upsert('domainSets', { id, name: name.value.trim(), identifier: original?.identifier ?? identifier.value, domains: [...domains.value], group: group.value, revision: (original?.revision ?? 0) + 1, updatedAt: new Date().toISOString() })
  if (!original) emit('update:selected', [...props.selected, id])
  show.value = false; message.success(tr('setSaved'))
}
function remove(id: string) {
  const refs = setReferences(workspace, id)
  if (props.selected.includes(id)) { message.warning(tr('setCurrent')); return }
  if (refs.profiles.length || refs.drafts.length) { message.warning(tr('setBlocked')); return }
  dialog.warning({ title: tr('setDeleteTitle'), content: tr('setShared'), positiveText: tr('delete'), negativeText: tr('cancel'), onPositiveClick: () => {
    const currentRefs = setReferences(workspace, id)
    if (currentRefs.profiles.length || currentRefs.drafts.length || props.selected.includes(id)) { message.warning(tr('setBlocked')); return false }
    if (workspace.scenario === 'error') { message.error(tr('failed')); return false }
    commands.remove('domainSets', id); message.success(tr('setDeleted'))
  } })
}
</script>
<template>
  <div class="heading"><div><h3>{{ tr('sets') }}</h3><p>{{ tr('setsHint') }}</p></div><NButton v-if="!readonly" @click="edit()">{{ tr('newSet') }}</NButton></div>
  <NCheckboxGroup :value="selected" :disabled="readonly" @update:value="emit('update:selected', $event.map(String))">
    <div class="sets"><article v-for="set in workspace.domainSets" :key="set.id" class="set-card">
      <NCheckbox :value="set.id"><strong>{{ set.name }}</strong></NCheckbox><p><code>{{ set.identifier }}</code> → {{ set.group }}</p><p>{{ set.domains.join(', ') }}</p>
      <div class="references"><RouterLink v-for="profile in setReferences(workspace, set.id).profiles" :key="profile.id" :to="entityLocation('profile', profile.id)">{{ profile.name }}</RouterLink><span v-for="draft in setReferences(workspace, set.id).drafts" :key="draft.id">{{ tr('saveDraft') }}: {{ draft.name }}</span></div>
      <div v-if="!readonly" class="actions"><NButton size="small" @click="edit(set.id)">{{ tr('edit') }}</NButton><NButton size="small" @click="remove(set.id)">{{ tr('delete') }}</NButton></div>
    </article></div>
  </NCheckboxGroup><p v-if="!workspace.domainSets.length">{{ tr('noSets') }}</p>
  <NModal v-model:show="show" preset="card" :title="tr(editingId ? 'editSet' : 'newSet')" class="domain-modal" style="width:min(640px,calc(100vw - 32px));max-height:90vh;overflow:auto">
    <NAlert type="warning" :bordered="false">{{ tr('setShared') }}</NAlert>
    <p v-if="refs.profiles.length">{{ tr('profiles') }}: {{ refs.profiles.map(p => p.name).join(', ') }}</p>
    <form @submit.prevent="save">
      <NFormItem :label="tr('setName')"><NInput v-model:value="name" :maxlength="100" /></NFormItem>
      <NFormItem :label="tr('identifier')"><NInput v-model:value="identifier" :disabled="!!editingId" :maxlength="80" /></NFormItem><p>{{ tr('setIdHint') }}</p>
      <NFormItem :label="tr('members')"><NInput v-model:value="members" type="textarea" :autosize="{minRows:4,maxRows:10}" :maxlength="260000" /></NFormItem>
      <NFormItem :label="tr('setGroup')"><NSelect v-model:value="group" :options="targets.map(value => ({label:value,value}))" /></NFormItem>
      <ul v-if="tried && problems.length" role="alert"><li v-for="problem in problems" :key="problem">{{ tr(`errors.${problem}`) }}</li></ul>
      <div class="actions"><NButton @click="show = false">{{ tr('cancel') }}</NButton><NButton type="primary" attr-type="submit">{{ tr('save') }}</NButton></div>
    </form>
  </NModal>
</template>
<style scoped>
.heading{display:flex;align-items:flex-start;gap:20px;justify-content:space-between}.heading h3{margin-top:0}.sets{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,290px),1fr));gap:12px}.set-card{padding:16px;border:1px solid var(--color-border);border-radius:10px;min-width:0}.set-card p{overflow-wrap:anywhere}.references{display:flex;gap:10px;flex-wrap:wrap;margin-bottom:12px}.references span{font-size:12px;color:var(--color-muted)}form{margin-top:20px}
@media(max-width:760px){:deep(.n-button){min-height:44px}}
</style>
