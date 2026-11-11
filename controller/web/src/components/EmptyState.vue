<script setup lang="ts">
import { NButton } from 'naive-ui'; import { useI18n } from 'vue-i18n'; defineProps<{ title?: string; description?: string; filtered?: boolean; actionLabel?: string }>(); defineEmits<{ action: []; clear: [] }>(); const { t } = useI18n()
</script><template><section class="panel state-panel"><h2>{{ title ?? t('common.empty') }}</h2><p v-if="description">{{ description }}</p><NButton v-if="filtered" @click="$emit('clear')">{{ t('common.clear') }}</NButton><NButton v-else-if="actionLabel" @click="$emit('action')">{{ actionLabel }}</NButton></section></template>
