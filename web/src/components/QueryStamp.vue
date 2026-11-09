<script setup lang="ts">
import { useI18n } from 'vue-i18n'; import { useFormat } from '../composables/useFormat'; defineProps<{ queriedAt?: string; expiresAt?: string; scope?: string }>(); const { t } = useI18n(); const { date } = useFormat()
</script><template><p class="query-stamp">{{ queriedAt ? t('common.lastQueried', { time: date(queriedAt) }) : t('common.notQueried') }} <span v-if="expiresAt && Date.parse(expiresAt) < Date.now()"> · {{ t('common.stale') }}</span><span v-if="scope"> · {{ t('common.scope', { scope }) }}</span></p></template>
