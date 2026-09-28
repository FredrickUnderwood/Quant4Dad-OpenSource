import { createHash } from 'node:crypto'

function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  if (value && typeof value === 'object') return `{${Object.keys(value).sort().map(key => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`
  return JSON.stringify(value)
}
export function argumentsHash(value) { return `sha256:${createHash('sha256').update(canonical(value)).digest('hex')}` }
const failure = code => new Error(code)

// Waiters contain only live control state. Decisions never become model input,
// session metadata, or journal fields. A cold Run is settled, not resumed here.
export class ApprovalWaiters {
  #pending = new Map()
  #decisions = new Map()
  wait(call, challenge, signal, observe) {
    const expires = Date.parse(challenge.expires_at)
    if (!/^[0-9a-f]{64}$/.test(challenge.approval_id) || challenge.arguments_hash !== argumentsHash(call.arguments) ||
        !['R2', 'R3'].includes(challenge.risk) || !Number.isFinite(expires) || expires <= Date.now() || expires - Date.now() > 300_000 ||
        this.#pending.has(challenge.approval_id) || this.#pending.size >= 128) throw failure('agent_approval_invalid')
    return new Promise((resolve, reject) => {
      let timer
      const cleanup = () => { clearTimeout(timer); signal.removeEventListener('abort', aborted); this.#pending.delete(challenge.approval_id) }
      const aborted = () => { cleanup(); reject(failure('agent_tool_cancelled')) }
      if (signal.aborted) return aborted()
      signal.addEventListener('abort', aborted, { once: true })
      timer = setTimeout(() => { cleanup(); reject(failure('agent_approval_timeout')) }, expires - Date.now())
      this.#pending.set(challenge.approval_id, { call, expires, decide: body => {
        cleanup()
        const digest = argumentsHash(body)
        this.#decisions.set(challenge.approval_id, { digest, expires })
        for (const [id, item] of this.#decisions) if (item.expires <= Date.now()) this.#decisions.delete(id)
        if (this.#decisions.size > 512) this.#decisions.delete(this.#decisions.keys().next().value)
        resolve(body.decision === 'allow_once' ? body.approval_receipt : undefined)
      } })
      try { observe('approval.required', { name: call.name, approval_id: challenge.approval_id, arguments_hash: challenge.arguments_hash,
        risk: challenge.risk, expires_at: new Date(expires).toISOString() }) }
      catch { cleanup(); reject(failure('agent_runtime_unavailable')) }
    })
  }
  decide(id, body) {
    const previous = this.#decisions.get(id)
    if (previous && previous.expires > Date.now() && previous.digest === argumentsHash(body)) return
    const pending = this.#pending.get(id)
    if (!pending || pending.expires <= Date.now() || pending.call.run_id !== body.run_id || pending.call.tool_call_id !== body.tool_call_id ||
        !['allow_once', 'reject'].includes(body.decision) ||
        (body.decision === 'allow_once' ? !/^[A-Za-z0-9_-]{86}$/.test(body.approval_receipt) : body.approval_receipt !== undefined)) throw failure('agent_approval_missing')
    pending.decide(body)
  }
}
