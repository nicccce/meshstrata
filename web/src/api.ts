export interface SystemInfo {
  component: 'controller'
  version: string
  commit: string
  build_time: string
  instance_id: string
}

export async function getSystem(signal: AbortSignal): Promise<SystemInfo> {
  const response = await fetch('/api/v1/system', { signal, cache: 'no-store' })
  if (!response.ok) throw new Error('SYSTEM_REQUEST_FAILED')
  const data: unknown = await response.json()
  if (typeof data !== 'object' || data === null) throw new Error('INVALID_SYSTEM_RESPONSE')
  const value = data as Record<string, unknown>
  if (value.component !== 'controller' || ['version', 'commit', 'build_time', 'instance_id'].some((key) => typeof value[key] !== 'string')) {
    throw new Error('INVALID_SYSTEM_RESPONSE')
  }
  return value as unknown as SystemInfo
}
