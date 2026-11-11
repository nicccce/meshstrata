import { computed, ref, onBeforeUnmount, watch, h } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { useDialog, NButton } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useWorkspace, resetEpoch } from '../demo/workspace'
import type { Draft } from '../domain'
export function useDraft(id: string, initial: Draft) {
 const { workspace, commands } = useWorkspace(); const dialog = useDialog(); const { t } = useI18n()
 const restored = workspace.drafts.find(d => d.id === id)
 const value = ref<Draft>(JSON.parse(JSON.stringify(restored ?? initial)))
 const baseline = ref(JSON.stringify(value.value))
 watch(resetEpoch, () => { value.value = JSON.parse(JSON.stringify(initial)); baseline.value = JSON.stringify(value.value) })
 const dirty = computed(() => JSON.stringify(value.value) !== baseline.value)
 function save() { commands.saveDraft(value.value); baseline.value = JSON.stringify(value.value) }
 function discard() { value.value = JSON.parse(baseline.value) }
 onBeforeRouteLeave(() => {
  if (!dirty.value) return true
  return new Promise<boolean>(resolve => {
   let settled = false
   const finish = (leave: boolean) => { if (!settled) { settled = true; resolve(leave) } }
   const d = dialog.warning({ title: t('common.dirty'), content: t('common.dirtyBody'),
    action: () => h('div', { class: 'actions' }, [
     h(NButton, { onClick: () => { finish(false); d.destroy() } }, { default: () => t('common.stay') }),
     h(NButton, { onClick: () => { discard(); finish(true); d.destroy() } }, { default: () => t('common.discard') }),
     h(NButton, { type: 'primary', onClick: () => { save(); finish(true); d.destroy() } }, { default: () => t('common.saveDraft') }),
    ]), onClose: () => finish(false), onMaskClick: () => finish(false), onAfterLeave: () => finish(false) })
  })
 })
 const unload = (event: BeforeUnloadEvent) => { if (dirty.value) { event.preventDefault(); event.returnValue = '' } }
 window.addEventListener('beforeunload', unload); onBeforeUnmount(() => window.removeEventListener('beforeunload', unload))
 return { value, dirty, save, discard }
}
