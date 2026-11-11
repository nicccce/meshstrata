<script setup lang="ts">
import EntityLink from './EntityLink.vue'; import { useI18n } from 'vue-i18n'; defineProps<{ nodes: readonly { id: string; name: string }[] }>(); const { t } = useI18n()
</script><template><ol class="route-path" :aria-label="t('common.path')"><li v-for="node in nodes" :key="node.id"><EntityLink kind="node" :id="node.id" :label="node.name" /></li></ol></template>
