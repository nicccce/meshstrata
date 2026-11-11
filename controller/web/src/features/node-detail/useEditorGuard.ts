import { h, onBeforeUnmount, type ComputedRef } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { NButton, useDialog } from 'naive-ui'
import { useI18n } from 'vue-i18n'

// Protocol/probe fields have no DraftData slot. Unsupported payloads must not be persisted.
export function useEditorGuard(dirty: ComputedRef<boolean>, discard: () => void, saveDraft?: () => boolean | void) {
  const dialog = useDialog()
  const { t } = useI18n()
  function confirmLeave(): Promise<boolean> | boolean {
    if (!dirty.value) return true
    return new Promise(resolve => {
      let settled = false
      const finish = (leave: boolean) => { if (!settled) { settled = true; resolve(leave) } }
      const modal = dialog.warning({ title: t('common.dirty'), content: t(saveDraft ? 'common.dirtyBody' : 'features.node-detail.unsavedBody'),
        action: () => h('div', { class: 'actions' }, [
          h(NButton, { onClick: () => { finish(false); modal.destroy() } }, { default: () => t('common.stay') }),
          h(NButton, { onClick: () => { discard(); finish(true); modal.destroy() } }, { default: () => t('common.discard') }),
          ...(saveDraft ? [h(NButton, { type: 'primary', onClick: () => { const saved = saveDraft(); finish(saved !== false); modal.destroy() } }, { default: () => t('common.saveDraft') })] : []),
        ]), onClose: () => finish(false), onMaskClick: () => finish(false), onAfterLeave: () => finish(false),
      })
    })
  }
  onBeforeRouteLeave(confirmLeave)
  onBeforeRouteUpdate((to, from) => to.params.nodeId !== from.params.nodeId ? confirmLeave() : true)
  const unload = (event: BeforeUnloadEvent) => { if (dirty.value) { event.preventDefault(); event.returnValue = '' } }
  window.addEventListener('beforeunload', unload)
  onBeforeUnmount(() => window.removeEventListener('beforeunload', unload))
  return { confirmLeave }
}
