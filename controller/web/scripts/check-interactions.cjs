const assert=require('node:assert/strict');
// Optional: run against a production preview with an isolated Playwright install.
const {chromium}=require(process.env.MESHSTRATA_PLAYWRIGHT_MODULE||'playwright');
const path=require('node:path');
const fs=require('node:fs/promises');
(async()=>{
const browser=await chromium.launch({executablePath:process.env.MESHSTRATA_CHROMIUM,args:['--no-sandbox']});
try {
 const page=await browser.newPage({viewport:{width:1440,height:1000}}); const errors=[];
 page.on('pageerror',e=>errors.push(e.message)); page.on('dialog',d=>d.accept());
 const go=async p=>{await page.goto((process.env.MESHSTRATA_PREVIEW_URL||'http://127.0.0.1:5174')+'/?flow='+Math.random()+'#'+p);await page.locator('h1').first().waitFor()};
 const click=async name=>page.getByRole('button',{name,exact:true}).click();
 const stored=()=>page.evaluate(()=>JSON.parse(localStorage.getItem('meshstrata.demo.v1')));
 await go('/routes/new?exitNodeId=hk&step=paths');
 await click('Recommend routes'); await page.locator('.candidate').first().getByRole('checkbox').click();
 await click('Next'); await click('Save draft'); await page.waitForURL(/draftId=/);
 const draftUrl=page.url();
 await click('Egress Routing'); await page.locator('.n-drawer').getByRole('switch').first().click();
 await page.getByRole('textbox',{name:'Policy name',exact:true}).fill('Browser drawer draft');
 await click('Save drawer draft');
 assert.ok((await stored()).drafts.some(d=>d.feature==='egress'&&d.data.egress.name==='Browser drawer draft'));
 await click('Cancel'); await page.reload(); await page.locator('h1').first().waitFor();
 await click('Egress Routing'); assert.equal(await page.getByRole('textbox',{name:'Policy name',exact:true}).inputValue(),'Browser drawer draft');
 await page.locator('.n-drawer').getByRole('switch').first().click(); await click('Save policy draft');
 await click('Next'); await click('Validate frontend draft · simulate');
 await page.getByRole('button',{name:/^Simulate publish/}).click(); await page.waitForURL(/activity\/releases\//);
 const after=await stored(); const created=after.routes.find(r=>r.name.startsWith('HK-01')&&r.id.startsWith('route-'))||after.routes.at(-1);
 assert.equal(created.state,'published');assert.ok(created.published);assert.ok(!created.published.policyId);
 const draftId=new URLSearchParams(draftUrl.split('?').at(-1)).get('draftId');assert.ok(!after.drafts.some(d=>d.id===draftId));
 console.log('Route flow passed: candidate → options → drawer draft save/reload → Node Default → publish → release; completed draft removed.');
 await go('/client-config/subscriptions/new?routeIds=direct&profileId=default');
 await page.getByRole('textbox',{name:'Name',exact:true}).fill('Browser subscription');
 await click('Continue'); await click('Continue'); await click('Create Rule Profile'); await page.waitForURL(/rule-profiles\/new/);
 await page.getByRole('textbox',{name:'Name',exact:true}).fill('Browser profile');
 await page.getByRole('textbox',{name:'Ordered rule input',exact:true}).fill('DOMAIN-SUFFIX,example.invalid,All-Proxies\nMATCH,All-Proxies');
 await click('Save Profile'); await page.waitForURL(/subscriptions\/new/);
 assert.ok((await page.locator('body').innerText()).includes('Browser profile'));
 await click('Back'); await click('Back'); assert.equal(await page.getByRole('textbox',{name:'Name',exact:true}).inputValue(),'Browser subscription');
 await click('Continue');await click('Continue');await click('Continue');await click('Generate Subscription');await page.waitForURL(/subscriptions\/sub-/);
 const result=await stored();const sub=result.subscriptions.find(s=>s.name==='Browser subscription');assert.ok(sub);assert.equal(sub.profileId,result.profiles.find(p=>p.name==='Browser profile').id);assert.deepEqual(sub.routeIds,['direct']);
 assert.deepEqual(errors,[]);console.log('Subscription flow passed: saved draft → create rule profile → return with selection/name/routes intact → review → generate. No page errors.');
 const output=path.resolve(process.env.MESHSTRATA_BROWSER_OUTPUT||'.local/browser-check/results'); await fs.mkdir(output,{recursive:true}); await page.screenshot({path:path.join(output,'subscription-created.png'),fullPage:true});
}finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
