import { ref, shallowRef } from 'vue'
import type { Issue } from '../domain'
export function useQueryState<T>() {
 const data = shallowRef<T>(); const loading = ref(false); const error = ref<Issue>(); const queriedAt = ref<string>()
 return { data, loading, error, queriedAt, async run(query: () => Promise<T>) {
  if (loading.value) return
  loading.value = true; error.value = undefined
  try { data.value = await query(); queriedAt.value = new Date().toISOString() }
  catch { error.value = { code: 'DEMO_ERROR' } }
  finally { loading.value = false }
 } }
}
