<script setup lang="ts">
import { useI18n } from 'vue-i18n'; import PageHeader from './PageHeader.vue'; import { pageLocation } from '../app/router'; const { t } = useI18n()
</script><template><PageHeader :title="t('common.notFound')" :description="t('common.missing')" /><RouterLink :to="pageLocation('overview')">{{ t('common.home') }}</RouterLink></template>
