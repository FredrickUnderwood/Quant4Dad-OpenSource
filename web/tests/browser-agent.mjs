// Real isolated Chrome + built UI + synthetic local API. No saved browser
// profile or model/provider credentials are used. Run after `npm run build`.
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, writeFile, realpath, rm, mkdir, rename } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { once } from 'node:events'
import assert from 'node:assert/strict'

const root = await realpath(await mkdtemp(join(tmpdir(), 'q4d-browser-'))), issues = []
let chrome, fixture, socket, diagnostic
const delay = ms => new Promise(resolve => setTimeout(resolve, ms))
async function until(fn) { const deadline = Date.now() + 20000; while (Date.now() < deadline) { try { const r = await fn(); if (r) return r } catch {} await delay(100) } throw new Error('browser_wait_timeout') }
try {
  fixture = spawn(process.execPath, [new URL('./assistant-fixture.mjs', import.meta.url).pathname], { env: { ...process.env, Q4D_ASSISTANT_P0: '1' }, stdio: ['ignore', 'pipe', 'pipe'] })
  let output = ''; fixture.stdout.on('data', c => output += c)
  const url = await until(() => output.match(/http:\/\/127\.0\.0\.1:\d+\/assistant/)?.[0])
  chrome = spawn(process.env.CHROME_BIN ?? (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : '/usr/bin/google-chrome'),
    ['--headless=new', '--disable-background-networking', '--disable-sync', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--window-size=1280,1000', '--user-data-dir=' + root, 'about:blank'], { stdio: 'ignore' })
  const port = await until(async () => (await readFile(join(root, 'DevToolsActivePort'), 'utf8')).split('\n')[0])
  const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json()
  socket = new WebSocket(pages.find(p => p.type === 'page').webSocketDebuggerUrl)
  await once(socket, 'open')
  let next = 0; const pending = new Map()
  socket.addEventListener('message', event => { const data = JSON.parse(event.data); if (data.id) {
    const p = pending.get(data.id); if (p) { clearTimeout(p.timer); pending.delete(data.id); data.error ? p.reject(new Error(data.error.message)) : p.resolve(data.result) }
  } else if (data.method === 'Runtime.exceptionThrown') issues.push(data.params.exceptionDetails.text) })
  const call = (method, params = {}) => new Promise((resolve, reject) => { const id = ++next; const timer = setTimeout(() => { pending.delete(id); reject(new Error('browser_command_timeout')) }, 20000); pending.set(id, { resolve, reject, timer }); socket.send(JSON.stringify({ id, method, params })) })
  const evaluate = async expression => { const value = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }); if (value.exceptionDetails) throw new Error('browser_evaluation_failed'); return value.result.value }
  diagnostic = () => evaluate('document.body.innerText')
  const button = name => `[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===${JSON.stringify(name)} || b.getAttribute('aria-label')===${JSON.stringify(name)})`
  const click = async name => { await until(() => evaluate(`!!(${button(name)} && !${button(name)}.disabled)`)); await evaluate(`${button(name)}.click()`) }
  const input = (selector, value) => evaluate(`(()=>{let e=document.querySelector(${JSON.stringify(selector)});Object.getOwnPropertyDescriptor(Object.getPrototypeOf(e),'value').set.call(e,${JSON.stringify(value)});e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));})()`)
  const text = value => until(() => evaluate(`document.body.innerText.includes(${JSON.stringify(value)})`))
  const titleIs = value => until(() => evaluate(`document.querySelector('.assistant-main h2')?.textContent === ${JSON.stringify(value)} && [...document.querySelectorAll('.assistant-session-list strong')].some(el => el.textContent === ${JSON.stringify(value)})`))
  const expandTool = () => evaluate(`document.querySelector('.assistant-tool-head[aria-expanded="false"]').click()`)
  const key = async (key, modifiers = 0) => {
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key, code: key, windowsVirtualKeyCode: key === 'Enter' ? 13 : 0, text: key === 'Enter' ? '\r' : '', modifiers })
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key, code: key, windowsVirtualKeyCode: key === 'Enter' ? 13 : 0, modifiers })
  }
  const screenshot = async name => {
    if (!process.env.Q4D_BROWSER_EVIDENCE) return
    // Let theme and layout transitions finish before capturing visual evidence.
    await delay(350)
    await mkdir(process.env.Q4D_BROWSER_EVIDENCE, { recursive: true })
    const shot = await call('Page.captureScreenshot', { format: 'png' })
    await writeFile(join(process.env.Q4D_BROWSER_EVIDENCE, name + '.png'), Buffer.from(shot.data, 'base64'))
  }
  await call('Runtime.enable'); await call('Page.enable'); await call('Page.navigate', { url })
  await text('检查模型连接'); await click('检查模型连接')
  await input('#assistant-profile', 'strategy_lab'); await click('开始对话')
  await until(() => evaluate('!!document.querySelector("textarea")'))
  await input('textarea', '创建一个策略用于审批验证'); await click('发送')
  await text('允许一次'); await call('Page.reload'); await text('允许一次')
  await click('允许一次'); await text('回答完成'); await expandTool(); await text('查看策略')
  await titleIs('自动总结主题 1')
  assert.equal(await evaluate('document.querySelector(".assistant-tool-result pre").textContent.includes("<script>")'), true)
  assert.equal(await evaluate('document.querySelectorAll(".assistant-tool-result script").length'), 0)
  await input('textarea', '这次拒绝保存'); await click('发送'); await text('拒绝本次调用'); await click('拒绝本次调用')
  await text('已拒绝本次调用，对话可以继续。')
  await titleIs('自动总结主题 2')
  await click('新对话'); await input('#assistant-profile', 'pipeline_builder'); await click('开始对话')
  await until(() => evaluate('!!document.querySelector("textarea")')); await input('textarea', '启用流水线用于 R3 审批验证'); await click('发送')
  await text('我已核对参数和自动化影响')
  await click('改名'); await input('input[aria-label="会话标题"]', '手动保留的流水线标题'); await click('保存')
  await titleIs('手动保留的流水线标题')
  assert.equal(await evaluate(`${button('允许一次')}.disabled`), true)
  await call('Page.reload'); await text('我已核对参数和自动化影响')
  assert.equal(await evaluate(`${button('允许一次')}.disabled`), true)
  await evaluate('document.querySelector(".assistant-approval input[type=checkbox]").click()')
  await until(() => evaluate(`!${button('允许一次')}.disabled`))
  if (process.env.Q4D_BROWSER_EVIDENCE) {
    await mkdir(process.env.Q4D_BROWSER_EVIDENCE, { recursive: true })
    const shot = await call('Page.captureScreenshot', { format: 'png' })
    await writeFile(join(process.env.Q4D_BROWSER_EVIDENCE, 'agent-r3-approval.png'), Buffer.from(shot.data, 'base64'))
  }
  await click('允许一次'); await text('回答完成'); await expandTool(); await text('查看流水线')
  await delay(4200); await titleIs('手动保留的流水线标题')

  // Exercise real keyboard behavior, waiting feedback, and cancellation with a draft.
  await click('新对话'); await input('#assistant-profile', 'text_only'); await click('开始对话')
  await until(() => evaluate('!!document.querySelector("textarea")'))
  await input('textarea', '停止前检查中文输入'); await evaluate('document.querySelector("textarea").focus()')
  await evaluate(`(()=>{const el=document.querySelector('textarea');el.dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));el.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,isComposing:true}));el.dispatchEvent(new CompositionEvent('compositionend',{bubbles:true}));el.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',keyCode:229,bubbles:true}));})()`)
  await key('Enter', 8)
  assert.equal(await evaluate('document.querySelector("textarea").value.endsWith("\\n")'), true)
  assert.equal((await (await fetch(new URL('/api/fixture/metrics', url))).json()).sends, 3)
  await key('Enter'); await text('正在思考')
  assert.equal(await evaluate(`!!(${button('停止生成')})`), true)
  await text('正在生成回答')
  await input('textarea', '接着刚才的内容继续'); await evaluate('document.querySelector("textarea").focus()'); await key('Enter')
  assert.equal((await (await fetch(new URL('/api/fixture/metrics', url))).json()).sends, 4)
  await screenshot('agent-thinking')
  await click('停止生成'); await text('已停止')
  assert.equal(await evaluate('document.querySelector("textarea").value'), '接着刚才的内容继续')
  await text('正在整理研究思路')
  await evaluate('document.querySelector("textarea").focus()'); await key('Enter'); await text('回答完成')
  await titleIs('自动总结主题 5')
  await until(() => evaluate('!!document.querySelector(".assistant-markdown table")'))
  assert.equal(await evaluate('document.querySelectorAll(".assistant-markdown h2").length'), 1)
  assert.equal(await evaluate('document.querySelectorAll(".assistant-markdown table tbody tr").length'), 2)
  assert.equal(await evaluate('!!document.querySelector(".assistant-markdown blockquote")'), true)
  assert.equal(await evaluate('!!document.querySelector(".assistant-markdown input[type=checkbox]:checked")'), true)
  assert.equal(await evaluate('document.querySelectorAll(".assistant-markdown script,.assistant-markdown iframe,.assistant-markdown a[href^=\\"javascript:\\"]").length'), 0)
  assert.equal(await evaluate('document.querySelector(".assistant-code-block code").textContent.includes("print")'), true)
  await call('Browser.grantPermissions', { origin: new URL(url).origin, permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite'] })
  await click('复制代码'); await text('已复制')
  assert.equal(await evaluate('navigator.clipboard.readText()'), 'print("hello, Quant4Dad")\n')
  await call('Page.reload'); await text('回答完成')
  assert.equal(await evaluate('document.querySelectorAll(".assistant-markdown h2").length'), 1)
  await click('浅色'); await evaluate(`document.querySelector('.assistant-markdown h2').scrollIntoView({block:'start'})`)
  await screenshot('agent-markdown-desktop')
  await click('深色'); await screenshot('agent-markdown-dark')
  await click('浅色')
  await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true })
  await evaluate(`document.querySelector('.assistant-markdown h2').scrollIntoView({block:'start'})`)
  assert.equal(await evaluate('document.documentElement.scrollWidth <= window.innerWidth'), true)
  assert.equal(await evaluate('document.querySelector(".assistant-input-box").getBoundingClientRect().bottom <= window.innerHeight'), true)
  assert.equal(await evaluate('[...document.querySelectorAll("header nav a")].every(a=>a.getBoundingClientRect().height < 45)'), true)
  await screenshot('agent-markdown-mobile')
  await call('Emulation.clearDeviceMetricsOverride')
  // The trusted result endpoint supplies the chart; no transcript HTML/options
  // are needed. The artifact survives reload and supports actual downloads.
  await click('新对话'); await input('#assistant-profile', 'research'); await click('开始对话')
  await until(() => evaluate('!!document.querySelector("textarea")')); await input('textarea', '展示 K线统计图'); await click('发送')
  await text('3 次命中 / 60 根有效样本'); await text('下载 K 线图')
  await until(() => evaluate('document.querySelector(".assistant-kline-analysis canvas")?.width > 0'))
  assert.equal(await evaluate('document.querySelectorAll(".assistant-kline-table tbody tr").length'), 3)
  await call('Page.reload'); await text('3 次命中 / 60 根有效样本')
  await evaluate('document.querySelector(".assistant-kline-analysis").scrollIntoView({block:"start"})')
  await screenshot('agent-kline-analysis-desktop')
  await call('Browser.setDownloadBehavior', { behavior: 'allow', downloadPath: root })
  await click('下载 K 线图'); await click('下载命中表')
  const png = await until(async () => await readFile(join(root, 'sh.600000-2025-01-01-2025-03-01-close_return-gte4.png')))
  assert.equal(png.subarray(1, 4).toString(), 'PNG')
  await rename(join(root, 'sh.600000-2025-01-01-2025-03-01-close_return-gte4.png'), join(root, 'baseline-chart.png'))
  const beforeHover = await evaluate('[...document.querySelectorAll(".assistant-kline-analysis canvas")].map(c=>c.toDataURL()).join()')
  const hover = await evaluate('(()=>{const r=document.querySelector(".assistant-kline-analysis canvas").getBoundingClientRect();return {x:r.left+r.width*.55,y:r.top+160}})()')
  await call('Input.dispatchMouseEvent', { type: 'mouseMoved', ...hover })
  await until(async () => (await evaluate('[...document.querySelectorAll(".assistant-kline-analysis canvas")].map(c=>c.toDataURL()).join()')) !== beforeHover)
  await click('下载 K 线图')
  const hoveredPNG = await until(async () => await readFile(join(root, 'sh.600000-2025-01-01-2025-03-01-close_return-gte4.png')))
  assert.deepEqual(hoveredPNG, png, 'exported PNG must exclude hovered tooltip and crosshair')
  const csv = await until(async () => await readFile(join(root, 'sh.600000-2025-01-01-2025-03-01-close_return-gte4-matches.csv'), 'utf8'))
  assert.equal(csv.trim().split('\r\n').length, 4)
  await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true })
  await evaluate('document.querySelector(".assistant-kline-analysis").scrollIntoView({block:"start"})')
  assert.equal(await evaluate('document.documentElement.scrollWidth <= window.innerWidth'), true)
  await screenshot('agent-kline-analysis-mobile')
  await call('Emulation.clearDeviceMetricsOverride')
  await call('Page.navigate', { url: new URL('/settings', url).href }); await text('Agent 模型与运行状态')
  await evaluate(`document.querySelector(${JSON.stringify('section[aria-label="Agent 模型设置"] input[type=checkbox]')}).click()`)
  await click('保存 Agent 配置'); await text('配置已保存，请检查模型连接。')
  const metrics = await (await fetch(new URL('/api/fixture/metrics', url))).json()
  assert.deepEqual(metrics, { sends: 6, cancels: 1, decisions: 3, modelWrites: 1, runs: 6, sessions: 4 })
  assert.deepEqual(issues, [])
  process.stdout.write(JSON.stringify({ browser: 'isolated real Chrome', checks: ['automatic titles after every send in header and sidebar', 'late title after answer completion', 'manual rename fences late title and survives reload', 'R2 approve and reject', 'R3 explicit checkbox', 'reload without resend or automatic approval', 'escaped Tool result and fixed product links', 'Enter send / Shift+Enter newline / IME protection', 'thinking and streaming feedback', 'cancel preserves response and next draft', 'Markdown blocks, tables, tasks and safe links', 'copy code', 'mobile composer and overflow', 'K-line chart and event table survive reload', 'real PNG and CSV downloads', 'mobile K-line chart layout', 'model settings save'], metrics }) + '\n')
} catch (e) {
  if (diagnostic) process.stderr.write(JSON.stringify({ error: e.message, page: await diagnostic().catch(() => 'unavailable'), issues }) + '\n')
  throw e
} finally {
  socket?.close(); chrome?.kill('SIGTERM'); fixture?.kill('SIGTERM')
  if (chrome?.exitCode === null) await Promise.race([once(chrome, 'exit'), delay(3000)])
  await rm(root, { recursive: true, force: true })
}
