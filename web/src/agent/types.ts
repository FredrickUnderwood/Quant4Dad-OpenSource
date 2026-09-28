export interface AgentSession {
  session_id: string; provider: string; model: string; profile: string; title: string;
  title_pending?: boolean; title_revision?: number;
  status: 'provisioning' | 'provisioning_failed' | 'active' | 'archived'; created_at: string;
}
export interface AgentModel { provider: string; model: string; status: string; reason: string; context_window?: number; max_output_tokens?: number; limits_source?: string }
export interface AgentRuntimeStatus { status: string; profile_source?: 'repository' | 'config'; profiles_aligned?: boolean; profiles: Array<{ id: string; tools: Array<{ name: string; available: boolean; reason?: string }> }>; runtime?: { version: string; adapter_version: string; dsh_version: string; image_digest: string; bridge_protocol: number } }
export interface TranscriptItem {
  seq: string; message_id: string; run_id: string; role: string; occurred_at: string;
  content: Array<{ type: string; text?: string; name?: string; arguments?: string; is_error?: boolean; content?: Array<{ type: string; text?: string }> }>; omitted_content: boolean;
}
export interface Transcript {
  session_id: string; snapshot_seq: string; items: TranscriptItem[]; has_more: boolean; next_before_seq: string | null;
}
export interface Run {
  session_id: string; run_id: string; message_id: string; state: string;
  terminal: boolean; durable: boolean; last_event_id: string;
}
export interface Submission { run_id: string; durable: boolean; status: string; run_url: string; events_url: string }
export interface AgentEvent { id: string; run_id: string; session_id: string; type: string; schema_version: number; data: Record<string, unknown> }
export interface LiveMessage { id: string; text: string; omitted: boolean; streaming?: boolean }
export interface LiveTool { id: string; name: string; status: 'proposed' | 'waiting_approval' | 'started' | 'completed' | 'failed' | 'unknown'; arguments?: string; argumentsOmitted?: boolean; code?: string; approval?: { id: string; hash: string; risk: string; expires: string } }
export interface AgentApproval { id: string; session_id: string; run_id: string; tool_call_id: string; tool_name: string; arguments_hash: string; risk: string; status: string; expires_at: string }
export interface AgentToolResult { run_id: string; tool_call_id: string; tool_name: string; status: string; risk?: string; execution_stage?: 'pre_dispatch'; error_code?: string; result?: { data: unknown; untrusted_data: boolean } }
export interface RunView { failure?: { code: string; budget?: { dimension: string; used: number; limit: number; requested: number } }; runID: string; sessionID: string; cursor: string; status: string; terminal: boolean; messages: LiveMessage[]; tools: LiveTool[]; order: Array<{ kind: 'message' | 'tool'; id: string }>; notice: string }
