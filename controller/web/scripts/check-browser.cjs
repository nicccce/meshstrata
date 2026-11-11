// Optional acceptance check. Requires Playwright and a running Vite server.
// MESHSTRATA_PLAYWRIGHT_MODULE may point to an isolated Playwright installation.
const assert = require('node:assert/strict')
const fs = require('node:fs/promises')
const path = require('node:path')
const { chromium } = require(process.env.MESHSTRATA_PLAYWRIGHT_MODULE || 'playwright')
const base = process.env.MESHSTRATA_PREVIEW_URL || 'http://127.0.0.1:5173'
const output = path.resolve(process.env.MESHSTRATA_BROWSER_OUTPUT || '.local/browser-check/results')
const pages = ['/overview', '/nodes', '/nodes/new', '/nodes/hk', '/routes', '/routes/new', '/routes/direct', '/routes/direct/edit', '/client-config/subscriptions', '/client-config/subscriptions/new', '/client-config/subscriptions/sub-active', '/client-config/subscriptions/sub-active/edit', '/client-config/rule-profiles', '/client-config/rule-profiles/new', '/client-config/rule-profiles/default/edit', '/activity', '/activity/releases/release-failed', '/settings', '/login']
;(async () => {
  await fs.mkdir(output, { recursive: true })
  const ts = require('typescript')
  const seedSource = await fs.readFile(path.resolve(__dirname, '../src/demo/seeds.ts'), 'utf8')
  const seedCode = ts.transpileModule(seedSource, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText
  const { createSeed } = await import(`data:text/javascript;base64,${Buffer.from(seedCode).toString('base64')}`)
  const browser = await chromium.launch({ headless: true, ...(process.env.MESHSTRATA_CHROMIUM ? { executablePath: process.env.MESHSTRATA_CHROMIUM } : {}), args: ['--no-sandbox'] })
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
    const page = await context.newPage()
    const errors = [], api = [], warnings = [], visited = [], overflows = []
    page.on('pageerror', e => errors.push(e.message))
    page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); if (m.type() === 'warning') warnings.push(m.text()) })
    page.on('request', r => { if (/\/(api(?:\/|$)|healthz|readyz)/.test(new URL(r.url()).pathname)) api.push(r.url()) })
    page.on('dialog', d => d.accept())
    const visit = async (route, screenshot) => {
      // Changing only a hash invokes the editor's leave guard. A distinct query
      // forces a fresh document so route smoke cannot pass on the previous page.
      await page.goto(`${base}/?acceptance=${visited.length}#${route}`, { waitUntil: 'networkidle' })
      await page.locator('h1, h2').first().waitFor()
      const title = await page.locator('h1, h2').first().innerText()
      assert.ok(title && !title.includes('features.'), route)
      const width = await page.evaluate(() => ({ document: document.documentElement.scrollWidth, viewport: innerWidth }))
      if (width.document > width.viewport + 2) overflows.push({ route, ...width, elements: await page.evaluate(() => [...document.querySelectorAll('main *')].filter(e => e.getBoundingClientRect().right > innerWidth + 2).slice(0, 8).map(e => ({ tag: e.tagName, class: e.className, right: Math.round(e.getBoundingClientRect().right) }))) })
      if (screenshot) await page.screenshot({ path: path.join(output, screenshot), fullPage: true })
      visited.push({ route, title, width: width.viewport })
    }
    for (const route of pages) await visit(route, ['/overview', '/routes/new', '/client-config/subscriptions/sub-active'].includes(route) ? `desktop-${route.split('/').at(-1)}.png` : undefined)
    for (const route of ['/nodes/missing', '/routes/missing', '/client-config/subscriptions/missing', '/client-config/rule-profiles/missing/edit', '/activity/releases/missing', '/unknown']) await visit(route)
    for (const scenario of ['empty', 'error', 'stale', 'offline', 'partial']) {
      await page.evaluate(seed => localStorage.setItem('meshstrata.demo.v1', JSON.stringify(seed)), createSeed(scenario))
      for (const route of ['/overview', '/nodes', '/routes', '/client-config/subscriptions', '/activity']) await visit(route)
    }
    await page.evaluate(seed => localStorage.setItem('meshstrata.demo.v1', JSON.stringify(seed)), createSeed())
    await page.setViewportSize({ width: 390, height: 844 })
    for (const route of pages) await visit(route, route === '/overview' ? 'mobile-overview.png' : undefined)
    await fs.writeFile(path.join(output, 'smoke.json'), JSON.stringify({ visited, errors, api, overflows, warnings: [...new Set(warnings)] }, null, 2))
    assert.deepEqual(errors, [], 'browser errors')
    assert.deepEqual(api, [], 'opening pages must not call business APIs')
    assert.deepEqual(overflows, [], 'viewport overflow')
    console.log(`Browser smoke passed: ${visited.length} page/scenario/viewport checks, no runtime errors, no automatic API requests. Results: ${output}`)
  } finally { await browser.close() }
})().catch(error => { console.error(error); process.exitCode = 1 })
