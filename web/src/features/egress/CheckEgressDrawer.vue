<script setup lang="ts">
import { NDrawer, NDrawerContent, NButton } from 'naive-ui'; import { useI18n } from 'vue-i18n'; import type { CheckEgressDrawerProps, CheckEgressDrawerEmits } from './types'
defineProps<CheckEgressDrawerProps>(); const emit = defineEmits<CheckEgressDrawerEmits>(); const { t } = useI18n()
function close() { emit('update:show', false); emit('cancel') }
</script><template><NDrawer :show="show" :width="480" style="max-width: 100vw" @update:show="value => { if (!value) close() }"><NDrawerContent :title="t('features.egress.check')" closable><p>{{ t('common.drawerHint') }}</p><template #footer><NButton @click="close">{{ t('common.close') }}</NButton></template></NDrawerContent></NDrawer></template>
