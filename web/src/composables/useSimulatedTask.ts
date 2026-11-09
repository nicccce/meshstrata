import { onBeforeUnmount, ref } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
export function useSimulatedTask() {
 const state = ref<'idle' | 'running' | 'success' | 'error' | 'cancelled'>('idle')
 let timer: ReturnType<typeof setTimeout> | undefined
 let settle: ((result: boolean) => void) | undefined
 function cancel() { if (timer !== undefined) clearTimeout(timer); timer = undefined; if (settle) { state.value = 'cancelled'; settle(false); settle = undefined } }
 function run(outcome: 'success' | 'error' = 'success', delay = 700): Promise<boolean> {
  cancel(); state.value = 'running'
  return new Promise(resolve => { settle = resolve; timer = setTimeout(() => { timer = undefined; state.value = outcome; settle = undefined; resolve(outcome === 'success') }, Math.max(0, Math.min(delay, 10000))) })
 }
 onBeforeUnmount(cancel); onBeforeRouteLeave(() => { cancel() })
 return { state, run, cancel }
}
