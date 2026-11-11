<script setup lang="ts">
import { entityLocation } from '../app/router'; defineProps<{ kind: 'node' | 'route' | 'subscription' | 'profile' | 'release'; id: string; label: string }>()
</script><template><RouterLink :to="entityLocation(kind, id)">{{ label }}</RouterLink></template>
