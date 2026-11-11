<script setup lang="ts">
import { useI18n } from 'vue-i18n'; defineProps<{ status: string; label?: string }>(); const { t, te } = useI18n()
</script><template><span class="status-badge">{{ label ?? (te(`status.${status}`) ? t(`status.${status}`) : t('common.unknown')) }}</span></template>
