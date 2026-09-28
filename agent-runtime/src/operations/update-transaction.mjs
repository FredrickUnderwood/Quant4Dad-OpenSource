// No rollback may hide new writes. The storage operation refuses that case;
// in response we leave both containers stopped for explicit reconciliation.
export async function updateTransaction(steps) {
  let stopped = false, candidate = false, activated = false
  await steps.preflight()
  await steps.drain()
  try {
    // stop may partially succeed; compensation must inspect/stop safely again.
    stopped = true; await steps.stopPrevious()
    await steps.stage()
    candidate = true; await steps.startCandidate()
    await steps.verifyCandidate()
    await steps.stopCandidate()
    await steps.seal()
    await steps.activate(); activated = true
    await steps.startCandidate()
    await steps.healthCandidate()
    return { status: 'updated', model_probe_required: true }
  } catch {
    try {
      if (candidate) await steps.stopCandidate()
      // Even a failed activation may have completed its atomic rename.
      if (activated || await steps.isCandidateActive()) await steps.rollback()
      if (stopped) { await steps.startPrevious(); await steps.healthPrevious() }
      return { status: 'restored_previous', model_probe_required: true }
    } catch { throw new Error('agent_update_reconciliation_required') }
  }
}
