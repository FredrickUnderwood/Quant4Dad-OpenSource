/** Only committed Bridge facts determine outcomes. An unloaded, unfinished Run
 * needs recovery; a GET never resumes an Agent or invents a terminal event. */
export function projectRun(binding, journal, { active = false, cancelling = false } = {}) {
  const events = journal.snapshot()
  const tools = new Map()
  for (const event of events) {
    if (event.type.startsWith('tool.') || event.type === 'approval.required') tools.set(event.data.tool_call_id, event.type)
  }
  const terminal = Boolean(journal.terminal)
  const durable = events[0]?.type === 'run.started'
  const state = terminal ? journal.terminal.slice(4)
    : !active ? 'recovering' : cancelling ? 'cancelling'
      : [...tools.values()].includes('approval.required') ? 'waiting_approval' : durable ? 'running' : 'pending'
  return { session_id: binding.session_id, run_id: binding.run_id, message_id: binding.message_id,
    execution_envelope_digest: binding.execution_envelope_digest, durable, state, terminal,
    last_event_id: events.at(-1)?.id ?? '0' }
}
