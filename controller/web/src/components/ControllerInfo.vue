<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'; import { NButton } from 'naive-ui'; import { useI18n } from 'vue-i18n'; import { getSystem, type SystemInfo } from '../api'; import QueryStamp from './QueryStamp.vue'; import { useFormat } from '../composables/useFormat'
const { t } = useI18n(); const { date } = useFormat(); const data = ref<SystemInfo>(); const loading = ref(false); const failed = ref(false); const queriedAt = ref<string>(); let request: AbortController | undefined; let timer: ReturnType<typeof setTimeout> | undefined
async function query() { if (loading.value) return; const current = new AbortController(); request = current; loading.value = true; failed.value = false; timer = setTimeout(() => current.abort(), 5000)
 try { const result = await getSystem(current.signal); if (request === current) { data.value = result; queriedAt.value = new Date().toISOString() } }
 catch { if (request === current) failed.value = true }
 finally { clearTimeout(timer); if (request === current) loading.value = false }
}
onBeforeUnmount(() => { request?.abort(); request = undefined; clearTimeout(timer) })
</script><template><section class="panel"><h2>{{ t('common.system') }}</h2><p>{{ t('common.systemHint') }}</p><NButton :loading="loading" @click="query">{{ t('common.query') }}</NButton><p v-if="failed" role="alert">{{ t('common.unavailable') }}</p><QueryStamp :queried-at="queriedAt" /><dl v-if="data" class="system-grid"><div><dt>{{ t('common.version') }}</dt><dd>{{ data.version }}</dd></div><div><dt>{{ t('common.commit') }}</dt><dd>{{ data.commit }}</dd></div><div><dt>{{ t('common.buildTime') }}</dt><dd>{{ date(data.build_time) }}</dd></div><div><dt>{{ t('common.instance') }}</dt><dd>{{ data.instance_id }}</dd></div></dl></section></template>
