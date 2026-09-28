import type { AgentSession } from './types.ts';

// Poll, send and archive reads may finish out of order. Never restore an old
// title over a newer generated title or a manual rename.
export function mergeSession(current: AgentSession | null, incoming: AgentSession): AgentSession {
  if (current?.session_id === incoming.session_id && (current.title_revision ?? 0) > (incoming.title_revision ?? 0)) return current;
  return incoming;
}
