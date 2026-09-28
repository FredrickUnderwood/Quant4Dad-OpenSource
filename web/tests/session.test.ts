import assert from 'node:assert/strict';
import test from 'node:test';
import { mergeSession } from '../src/agent/session.ts';
import type { AgentSession } from '../src/agent/types.ts';

const session: AgentSession = { session_id: 'one', title: '原标题', title_revision: 0, provider: 'fixture', model: 'fixture', profile: 'text_only', status: 'active', created_at: '' };

test('generated titles and manual renames survive late metadata responses', () => {
  const pending = { ...session, title_pending: true, title_revision: 1 };
  const generated = { ...session, title: '趋势策略', title_pending: false, title_revision: 2 };
  const manual = { ...generated, title: '手动标题', title_revision: 4 };
  assert.deepEqual(mergeSession(pending, generated), generated);
  assert.deepEqual(mergeSession(generated, pending), generated);
  assert.deepEqual(mergeSession(manual, generated), manual);
  assert.deepEqual(mergeSession(manual, { ...generated, session_id: 'two' }).session_id, 'two');
  assert.deepEqual(mergeSession(null, session), session);
});
