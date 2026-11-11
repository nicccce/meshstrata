/** Canonical UI locale. Entity data and protocol identifiers are never translated. */
export type UiLocale = 'en' | 'zh-CN'
export function normalizeLocale(value: unknown): UiLocale {
  return typeof value === 'string' && /^zh(?:[-_]|$)/i.test(value.trim()) ? 'zh-CN' : 'en'
}
export function resolveLocale(saved?: unknown, languages: readonly string[] = typeof navigator === 'undefined' ? [] : navigator.languages): UiLocale {
  // A malformed optional preference must not prevent the whole workspace loading.
  if (saved != null && typeof saved !== 'string') return 'en'
  return normalizeLocale((typeof saved === 'string' ? saved.trim() : '') || languages[0] || 'en')
}
