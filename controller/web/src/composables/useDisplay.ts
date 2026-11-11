import { useI18n } from 'vue-i18n'
/** Translate known presentation enums without changing persisted observation data. */
export function useDisplay() {
  const { t, te } = useI18n()
  function display(group: 'scope' | 'method' | 'timing' | 'environment', value?: string) {
    if (!value) return t('common.unknown')
    const key = `data.${group}.${value}`
    // Unknown machine identifiers remain inspectable, as do user-supplied values.
    return te(key) ? t(key) : value
  }
  return { display }
}
