import { resolveLocale } from '../../locale'
import type { Settings } from '../../domain'

export const sections = ['general', 'ports', 'relay', 'location', 'certificates', 'client', 'logs', 'admin'] as const
export type Section = typeof sections[number]
export type Form = Required<Pick<Settings, 'environment' | 'allowUdpTransport' | 'requireUdpRelay' | 'measurementTtlHours' | 'failureTtlMinutes' | 'locale' | 'webPort' | 'controlPort' | 'relay' | 'locationService' | 'certificates' | 'localClient' | 'logs'>> & Pick<Settings, 'defaultPortStart' | 'defaultPortEnd' | 'logLevel'>
export type Errors = Record<string, string>
export function initialForm(settings: Settings): Form {
  return {
    environment: settings.environment ?? 'weak_regulation', allowUdpTransport: settings.allowUdpTransport ?? true,
    requireUdpRelay: settings.requireUdpRelay ?? false, measurementTtlHours: settings.measurementTtlHours ?? 96,
    failureTtlMinutes: settings.failureTtlMinutes ?? 5, locale: resolveLocale(settings.locale),
    defaultPortStart: settings.defaultPortStart, defaultPortEnd: settings.defaultPortEnd,
    webPort: settings.webPort ?? 8443, controlPort: settings.controlPort ?? 9443,
    relay: { ...settings.relay, enabled: settings.relay?.enabled ?? false, endpoint: settings.relay?.endpoint ?? 'relay.example.invalid:9444', mappingPool: { ...(settings.relay?.mappingPool ?? { start: 30000, end: 30999 }) } },
    locationService: { ...(settings.locationService ?? { provider: 'demo', configured: false, preserveManual: true }) },
    certificates: { ...(settings.certificates ?? { domain: '', provider: 'demo', method: 'dns01', status: 'not_checked' }) },
    localClient: { ...(settings.localClient ?? { address: 'http://127.0.0.1:9090', paired: false, allowedOrigin: '', rawRtt: false }) },
    logLevel: settings.logLevel, logs: { ...(settings.logs ?? { fileMiB: 5, fileCount: 4, totalMiB: 20, retentionDays: 7, debugMinutes: 0 }) },
  }
}
export function sectionPatch(section: Section, form: Form): Partial<Settings> {
  switch (section) {
    case 'general': return { environment: form.environment, allowUdpTransport: form.allowUdpTransport, requireUdpRelay: form.requireUdpRelay, measurementTtlHours: form.measurementTtlHours, failureTtlMinutes: form.failureTtlMinutes }
    case 'ports': return { defaultPortStart: form.defaultPortStart, defaultPortEnd: form.defaultPortEnd, webPort: form.webPort, controlPort: form.controlPort }
    case 'relay': return { relay: JSON.parse(JSON.stringify(form.relay)) }
    case 'location': return { locationService: { ...form.locationService, configured: false } }
    case 'certificates': return { certificates: { ...form.certificates, status: 'not_checked' } }
    case 'client': return { localClient: { ...form.localClient } }
    case 'logs': return { logLevel: form.logLevel, logs: { ...form.logs } }
    case 'admin': return {}
  }
}
const port = (v: number) => Number.isInteger(v) && v >= 1 && v <= 65535
export function loopbackUrl(value: string): boolean {
  try { const u = new URL(value); return ['http:', 'https:'].includes(u.protocol) && ['127.0.0.1', '[::1]', 'localhost'].includes(u.hostname) && !u.username && !u.password && !u.search && !u.hash && u.pathname === '/' }
  catch { return false }
}
export function validDomain(value: string): boolean { return value.length <= 253 && /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/i.test(value) }
export function validEndpoint(value: string): boolean {
  if (!/^(?:[a-z0-9.-]+|\[[a-f0-9:]+\]):[0-9]{1,5}$/i.test(value)) return false
  try { const u = new URL(`https://${value}`); return !!u.hostname && port(Number(value.slice(value.lastIndexOf(':') + 1))) }
  catch { return false }
}
export function validate(section: Section, form: Form, relayEligible: boolean, mappingPorts: readonly number[]): Errors {
  const errors: Errors = {}
  const range = (start: number, end: number, field: string) => { if (!port(start) || !port(end) || start > end) errors[field] = 'range' }
  switch (section) {
    case 'general':
      if (!Number.isInteger(form.measurementTtlHours) || form.measurementTtlHours < 1 || form.measurementTtlHours > 8760) errors.measurementTtlHours = 'hours'
      if (!Number.isInteger(form.failureTtlMinutes) || form.failureTtlMinutes < 1 || form.failureTtlMinutes > 1440) errors.failureTtlMinutes = 'minutes'
      break
    case 'ports':
      range(form.defaultPortStart, form.defaultPortEnd, 'portPool')
      if (!port(form.webPort)) errors.webPort = 'port'
      if (!port(form.controlPort)) errors.controlPort = 'port'
      if (form.webPort === form.controlPort) errors.controlPort = 'conflict'
      if ([form.webPort, form.controlPort].some(p => p >= form.defaultPortStart && p <= form.defaultPortEnd)) errors.portPool = 'reserved'
      break
    case 'relay':
      if (form.relay.enabled && !relayEligible) errors.relay = 'relay'
      if (!validEndpoint(form.relay.endpoint)) errors.endpoint = 'endpoint'
      range(form.relay.mappingPool.start, form.relay.mappingPool.end, 'mappingPool')
      if (mappingPorts.some(p => p < form.relay.mappingPool.start || p > form.relay.mappingPool.end)) errors.mappingPool = 'mapping'
      if ([form.webPort, form.controlPort].some(p => p >= form.relay.mappingPool.start && p <= form.relay.mappingPool.end)) errors.mappingPool = 'reserved'
      break
    case 'location': if (!form.locationService.provider.trim() || form.locationService.provider.length > 80) errors.provider = 'required'; break
    case 'certificates':
      if (!validDomain(form.certificates.domain)) errors.domain = 'domain'
      if (form.certificates.method === 'dns01' && !form.certificates.provider.trim()) errors.provider = 'required'
      break
    case 'client':
      if (!loopbackUrl(form.localClient.address)) errors.address = 'loopback'
      try { const u = new URL(form.localClient.allowedOrigin); if (!['http:', 'https:'].includes(u.protocol) || u.origin !== form.localClient.allowedOrigin || u.username || u.password) errors.allowedOrigin = 'origin' }
      catch { errors.allowedOrigin = 'origin' }
      break
    case 'logs':
      for (const [key, max] of [['fileMiB', 1024], ['fileCount', 100], ['totalMiB', 10240], ['retentionDays', 365]] as const) {
        if (!Number.isInteger(form.logs[key]) || form.logs[key] < 1 || form.logs[key] > max) errors[key] = 'positive'
      }
      if (form.logs.totalMiB < form.logs.fileMiB) errors.totalMiB = 'logTotal'
      if (form.logLevel === 'debug' && (!Number.isInteger(form.logs.debugMinutes) || form.logs.debugMinutes < 1 || form.logs.debugMinutes > 60)) errors.debugMinutes = 'debug'
      break
  }
  return errors
}
