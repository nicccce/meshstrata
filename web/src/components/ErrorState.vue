<script setup lang="ts">
import { NButton } from 'naive-ui'; import { useI18n } from 'vue-i18n'; import { issueMessageKey, type Issue } from '../domain'; defineProps<{ issue: Issue; retryable?: boolean }>(); defineEmits<{ retry: [] }>(); const { t } = useI18n()
</script><template><section class="panel" role="alert"><p>{{ t(issueMessageKey(issue.code), issue.params ?? {}) }}</p><NButton v-if="retryable" @click="$emit('retry')">{{ t('common.retry') }}</NButton></section></template>
