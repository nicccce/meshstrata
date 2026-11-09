<script setup lang="ts">
import { NButton } from 'naive-ui'; import { useI18n } from 'vue-i18n'; defineProps<{ state: 'idle' | 'running' | 'success' | 'error' | 'cancelled' }>(); defineEmits<{ cancel: [] }>(); const { t } = useI18n()
</script><template><div role="status" class="actions">{{ t('common.task') }}: {{ t(`common.${state}`) }}<NButton v-if="state === 'running'" @click="$emit('cancel')">{{ t('common.cancel') }}</NButton></div></template>
