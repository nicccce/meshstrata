import assert from 'node:assert/strict'
import { readFile, readdir, mkdtemp, writeFile, rm } from 'node:fs/promises'
import { resolve, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import ts from 'typescript'
import { createI18n } from 'vue-i18n'
import { parse } from '@vue/compiler-sfc'
const root = fileURLToPath(new URL('../', import.meta.url))
const src = resolve(root, 'src')
async function resource(file) { return (await import(`data:text/javascript,${encodeURIComponent(await readFile(file, 'utf8'))}`)).default }
function flatten(value, prefix = '', result = {}) {
  for (const [key, item] of Object.entries(value)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (typeof item === 'string') result[path] = item
    else { assert.ok(item && typeof item === 'object', path); flatten(item, path, result) }
  }
  return result
}
const modules = ['common', ...(await readdir(resolve(src, 'features'))).filter(name => !name.includes('.'))]
const messages = { en: {}, 'zh-CN': {} }
const identicalAllowed = new Set(['Meshstrata', 'English', 'REJECT', 'Shadowsocks 2022', 'Trojan + TLS', 'VLESS + REALITY / Vision', 'VLESS + REALITY/Vision', 'Hysteria 2', 'TCP', 'UDP / QUIC', 'TCP + UDP', 'REALITY short ID', 'Relay', 'Agent / Mihomo', 'Agent demo-agent / Mihomo demo-pinned', '{value} B', 'CPU', '{value}%', '{value}/s', '{value} ms', 'DNS-01', 'TUN + fake-IP', 'responseStart − requestStart'])
let count = 0
for (const name of modules) {
  const directory = name === 'common' ? resolve(src, 'locales') : resolve(src, 'features', name, 'locales')
  const en = await resource(resolve(directory, 'en.ts')), zh = await resource(resolve(directory, 'zh-CN.ts'))
  const a = flatten(en), b = flatten(zh)
  assert.deepEqual(Object.keys(b).sort(), Object.keys(a).sort(), `${name}: exact key parity`)
  for (const [key, value] of Object.entries(a)) {
    assert.ok(b[key].trim(), `${name}.${key}: nonempty Chinese`)
    const parameters = text => [...text.matchAll(/\{([A-Za-z][\w]*)\}/g)].map(match => match[1]).sort()
    assert.deepEqual(parameters(b[key]), parameters(value), `${name}.${key}: interpolation parity`)
    assert.ok(value !== b[key] || identicalAllowed.has(value), `${name}.${key}: untranslated prose`)
    count++
  }
  for (const [locale, value] of [['en', en], ['zh-CN', zh]]) {
    if (name === 'common') Object.assign(messages[locale], value)
    else { messages[locale].features ??= {}; messages[locale].features[name] = value }
  }
}
const flat = flatten(messages.en)
const warnings = []
const i18n = createI18n({ legacy: false, locale: 'en', fallbackLocale: false, messages, missing: (_locale, key) => { warnings.push(key) } })
// Compile and interpolate every message in both languages, including rarely reached failures.
for (const locale of ['en', 'zh-CN']) {
  i18n.global.locale.value = locale
  for (const [key, value] of Object.entries(flat)) {
    const parameters = Object.fromEntries([...value.matchAll(/\{([A-Za-z][\w]*)\}/g)].map(match => [match[1], `SENTINEL_${match[1]}`]))
    const rendered = i18n.global.t(key, parameters)
    assert.notEqual(rendered, key, `${locale}.${key}: resolves`)
    for (const sentinel of Object.values(parameters)) assert.ok(rendered.includes(sentinel), `${locale}.${key}: interpolates ${sentinel}`)
  }
}
assert.deepEqual(warnings, [])
function requireKeys(prefix, values) { for (const value of values) assert.ok(flat[`${prefix}.${value}`], `Dynamic key: ${prefix}.${value}`) }
const releaseStates = ['draft','validating','applying','verifying','published','failed','rolling_back','rolled_back','rollback_pending','rollback_failed']
for (const feature of ['routes', 'node-detail']) requireKeys(`features.${feature}.${feature === 'routes' ? 'states' : 'releaseStates'}`, releaseStates)
requireKeys('status', ['online','offline','unknown','not_checked','verified','failed','incomplete','draft','published','pending','active','stale'])
requireKeys('features.activity.states', [...releaseStates, 'node_sync_pending','applied_unverified','none','succeeded','cancelled','running','confirmed','not_checked','online','offline','active','revoked','expired','pending','unknown','cleanup_pending'])
requireKeys('features.egress', ['observed','failed','not_supported','notChecked','destination','egress_ip','dns_path','client_coverage'])
requireKeys('features.subscriptions', ['active','expiring','expired','revoked','synced','pending','confirmed','pendingCleanup','none','cumulative_bytes','connection_count','active_snapshot','unavailable'])
requireKeys('features.settings.errors', ['listenerConflict','required','port','range','conflict','reserved','hours','minutes','relay','endpoint','mapping','domain','loopback','origin','positive','logTotal','debug','secret','pem','current','password','confirm','relayDisable','controllerPort'])
// Resolve static calls in scripts and template expressions, including key/prefix aliases.
let staticCalls = 0, dynamicCalls = 0
async function scan(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const file = resolve(directory, entry.name)
    if (entry.isDirectory()) { if (entry.name !== 'locales') await scan(file); continue }
    if (!/\.(vue|ts)$/.test(file)) continue
    const source = await readFile(file, 'utf8')
    const descriptor = file.endsWith('.vue') ? parse(source).descriptor : undefined
    const script = descriptor ? descriptor.scriptSetup?.content ?? descriptor.script?.content ?? '' : source
    const aliases = {}
    for (const match of script.matchAll(/(?:const|let)\s+(key|prefix)\s*=\s*(['"])([^'"]+)\2/g)) aliases[match[1]] = match[3]
    const feature = file.match(/\/features\/([^/]+)\//)?.[1]
    function literal(node) {
      if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text
      if (ts.isIdentifier(node)) return aliases[node.text]
      if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken) { const a = literal(node.left), b = literal(node.right); if (a !== undefined && b !== undefined) return a + b }
      if (ts.isTemplateExpression(node)) { let result = node.head.text; for (const span of node.templateSpans) { const part = literal(span.expression); if (part === undefined) return; result += part + span.literal.text } return result }
    }
    function inspect(text) {
      const ast = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
      function leaves(node) {
        if (!node) return []
        if (ts.isStringLiteral(node)) return [node.text]
        if (ts.isConditionalExpression(node)) return [...leaves(node.whenTrue), ...leaves(node.whenFalse)]
        return []
      }
      function dynamic(prefix, expression) {
        for (const key of leaves(expression)) { requireKeys(prefix, [key]); dynamicCalls++ }
      }
      function visit(node) {
        if (ts.isCallExpression(node)) {
          const name = ts.isIdentifier(node.expression) ? node.expression.text : ''
          if (feature === 'subscriptions' && file.endsWith('/model.ts') && ['error','warn'].includes(name)) dynamic('features.subscriptions.validation', node.arguments[0])
          if (feature === 'node-detail' && name === 'issue') dynamic('features.node-detail.errors', node.arguments[0])
          if (feature === 'route-editor' && ts.isPropertyAccessExpression(node.expression) && node.expression.expression.getText(ast) === 'issues' && node.expression.name.text === 'push') dynamic('features.route-editor.errors', node.arguments[0])
        }
        if (ts.isPropertyAssignment(node) && node.name.getText(ast) === 'code' && feature === 'rule-profiles') dynamic('features.rule-profiles.errors', node.initializer)
        if (ts.isPropertyAssignment(node) && node.name.getText(ast) === 'key' && feature === 'nodes') dynamic('features.nodes.wizard', node.initializer)
        if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.EqualsToken && feature === 'settings' && node.left.getText(ast).startsWith('errors.')) dynamic('features.settings.errors', node.right)
        if (ts.isReturnStatement(node) && feature === 'egress' && file.endsWith('/logic.ts')) dynamic('features.egress', node.expression)
        if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && ['t','tr','text','f'].includes(node.expression.text) && node.arguments.length) {
          let key = literal(node.arguments[0])
          if (key !== undefined && node.expression.text !== 't' && feature) key = `features.${feature}.${key}`
          if (key !== undefined) {
            assert.ok(flat[key], `${file}: unresolved static key ${key}`); staticCalls++
            const required = [...flat[key].matchAll(/\{([A-Za-z][\w]*)\}/g)].map(match => match[1])
            const argument = node.arguments[1]
            if (required.length && (!argument || (ts.isObjectLiteralExpression(argument) && !argument.properties.some(ts.isSpreadAssignment)))) {
              const provided = argument?.properties.filter(property => ts.isPropertyAssignment(property) || ts.isShorthandPropertyAssignment(property)).map(property => property.name.getText(ast).replace(/^['"]|['"]$/g, '')) ?? []
              for (const parameter of required) assert.ok(provided.includes(parameter), `${file}: ${key} requires parameter ${parameter}`)
            }
          }
        }
        ts.forEachChild(node, visit)
      }
      visit(ast)
    }
    inspect(script)
    function template(node) {
      if (node.type === 5) inspect(node.content.content)
      if (node.type === 1) for (const prop of node.props) if (prop.type === 7 && prop.exp) inspect(prop.exp.content)
      for (const child of node.children ?? []) template(child)
    }
    if (descriptor?.template?.ast) template(descriptor.template.ast)
    assert.ok(!/\.toLocaleString\(\)/.test(source), `${file}: locale-independent formatting`)
  }
}
await scan(src)
const temp = await mkdtemp(resolve(root, 'node_modules/.cache/i18n-'))
try {
  const raw = await readFile(resolve(src, 'locale.ts'), 'utf8')
  await writeFile(resolve(temp, 'locale.mjs'), ts.transpileModule(raw, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText)
  const { resolveLocale, normalizeLocale } = await import(pathToFileURL(resolve(temp, 'locale.mjs')))
  for (const language of ['zh','zh-CN','zh-TW','zh-HK','zh-Hans','zh_Hant','ZH-cn']) assert.equal(normalizeLocale(language), 'zh-CN')
  for (const language of ['en','en-US','fr-FR','invalid', null]) assert.equal(normalizeLocale(language), 'en')
  assert.equal(resolveLocale(undefined, ['zh-TW','en-US']), 'zh-CN')
  assert.equal(resolveLocale(undefined, ['fr-FR','zh-CN']), 'en')
  assert.equal(resolveLocale('en', ['zh-CN']), 'en')
  assert.equal(resolveLocale('zh-HK', ['en']), 'zh-CN')
  assert.equal(resolveLocale('unsupported', ['zh-CN']), 'en')
  for (const saved of [42, false, { language: 'zh-CN' }]) assert.equal(resolveLocale(saved, ['zh-CN']), 'en', 'malformed preference never crashes startup')
  assert.equal(resolveLocale(null, ['zh-CN']), 'zh-CN', 'absent preference follows the browser')
  assert.equal(resolveLocale(undefined, []), 'en')
  const model = await readFile(resolve(src, 'features/settings/model.ts'), 'utf8')
  await writeFile(resolve(temp, 'settings.mjs'), ts.transpileModule(model, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText.replace("from '../../locale'", "from './locale.mjs'"))
  const { initialForm, sectionPatch } = await import(pathToFileURL(resolve(temp, 'settings.mjs')))
  const form = initialForm({ defaultPortStart: 20000, defaultPortEnd: 29999, relayNodeId: 'relay', targetDns: [], logLevel: 'warn' })
  assert.ok(!Object.hasOwn(sectionPatch('general', form), 'locale'), 'saving unrelated settings does not create a language preference')
} finally { await rm(temp, { recursive: true, force: true }) }
console.log(`i18n checks passed: ${modules.length} modules, ${count} matching keys and interpolation sets, both languages compiled, ${staticCalls} static calls, ${dynamicCalls} dynamic validation emissions, state matrices and locale resolution.`)
