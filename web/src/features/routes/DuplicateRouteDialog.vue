<script setup lang="ts">
import { computed } from 'vue'
import { NModal, NInput, NButton, NFormItem, NAlert } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useDraft } from '../../composables/useDraft'
import { useWorkspace } from '../../demo/workspace'
import { pageLocation } from '../../app/router'
import { duplicateDraft, type RouteSnapshot } from './route-model'
const props = defineProps<{ source: RouteSnapshot; draftId: string }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n(); const router = useRouter(); const { workspace } = useWorkspace()
const { value, save, discard } = useDraft(props.draftId, duplicateDraft(workspace, props.source, props.draftId, t('features.routes.copyName', { name: props.source.name })))
const error = computed(() => !value.value.name.trim() || value.value.name.trim().length > 80 ? t('features.routes.nameError') : '')
function cancel() { discard(); emit('close') }
function submit() {
  if (error.value) return
  value.value.name = value.value.name.trim()
  const path = value.value.data?.route?.selectedPaths[0]
  if (path) path.name = value.value.name
  save(); emit('close')
  void router.push(pageLocation('route-new', {}, { draftId: props.draftId, step: 'options' }))
}
</script>
<template>
  <NModal :show="true" preset="card" :title="t('features.routes.duplicate')" style="width: min(560px, 94vw)" @update:show="show => { if (!show) cancel() }">
    <NAlert type="info" :bordered="false">{{ t('features.routes.duplicateHint') }}</NAlert>
    <form class="copy-form" @submit.prevent="submit">
      <NFormItem :label="t('features.routes.name')" :validation-status="error ? 'error' : undefined" :feedback="error">
        <NInput v-model:value="value.name" autofocus :maxlength="80" :input-props="{ id: 'route-copy-name', 'aria-label': t('features.routes.name') }" />
      </NFormItem>
      <div class="actions"><NButton @click="cancel">{{ t('common.cancel') }}</NButton><NButton type="primary" attr-type="submit" :disabled="!!error">{{ t('features.routes.saveOpen') }}</NButton></div>
    </form>
  </NModal>
</template>
<style scoped>.copy-form { margin-top: 20px; }</style>
