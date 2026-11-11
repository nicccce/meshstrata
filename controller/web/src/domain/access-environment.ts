import type { AccessEnvironment } from './index'

export const networkRegulationLevels: readonly AccessEnvironment[] = ['strong_regulation', 'weak_regulation']

export function migrateAccessEnvironmentValue(value: string): string {
  if (value === 'mainland_china') return 'strong_regulation'
  if (value === 'outside_mainland_china') return 'weak_regulation'
  return value
}

export function normalizeAccessEnvironment(value: unknown): AccessEnvironment {
  return typeof value === 'string' && migrateAccessEnvironmentValue(value) === 'strong_regulation' ? 'strong_regulation' : 'weak_regulation'
}

// Preserve existing demo records/drafts while retiring geographic categories.
// This maps stored choices only; no value is inferred from a node's location/IP.
export function migrateAccessEnvironments(value: unknown): void {
  if (!value || typeof value !== 'object') return
  for (const [key, child] of Object.entries(value)) {
    if (key === 'environment' && typeof child === 'string') {
      (value as Record<string, unknown>)[key] = migrateAccessEnvironmentValue(child)
    } else migrateAccessEnvironments(child)
  }
}
