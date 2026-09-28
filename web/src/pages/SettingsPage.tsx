import { useEffect, useState } from 'react';
import { api } from '../api';
import type {
  LLMProviderInput, NewsSourceSetting, NewsSourceStatus,
  EmailSettingInput, FeishuSettingInput,
} from '../types';
import { NEWS_SOURCE_LABEL } from '../types';
import Select from '../components/Select';
import { AgentModelSettings } from '../agent/ModelSettings';

// The settings page's section list, shared by the index on the left and the continuous ledger on
// the right. Adding a section means adding one row here.
const MODULES = [
  { id: 'llm', no: '01', label: '大模型', hint: 'Provider 接入' },
  { id: 'news', no: '02', label: '资讯数据源', hint: '扩展接入' },
  { id: 'notify', no: '03', label: '触达通道', hint: '邮箱 · 飞书' },
] as const;

export default function SettingsPage() {
  const active = useScrollSpy(MODULES.map((m) => m.id));

  const jump = (id: string) => {
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  };

  return (
    <div className="settings-shell">
      <nav className="settings-index" aria-label="设置目录">
        <div className="index-label">目录 · Index</div>
        <ol>
          {MODULES.map((m) => (
            <li key={m.id}>
              <button
                type="button"
                className={`index-link${active === m.id ? ' active' : ''}`}
                aria-current={active === m.id ? 'true' : undefined}
                onClick={() => jump(m.id)}
              >
                <span className="ix-no">{m.no}</span>
                <span>
                  <span className="ix-name">{m.label}</span>
                  <span className="ix-hint">{m.hint}</span>
                </span>
              </button>
            </li>
          ))}
        </ol>
      </nav>

      <div className="settings-sheet">
        <LLMSettings />
        <AgentModelSettings />
        <NewsSourceSettings />
        <NotifySettings />
      </div>
    </div>
  );
}

// useScrollSpy tracks which section is currently near the top of the viewport with an
// IntersectionObserver and reports its id, so the index can highlight it.
function useScrollSpy(ids: string[]) {
  const [active, setActive] = useState(ids[0] ?? '');
  const key = ids.join(',');
  useEffect(() => {
    const els = ids
      .map((id) => document.getElementById(id))
      .filter((el): el is HTMLElement => el != null);
    if (els.length === 0) return;
    const obs = new IntersectionObserver(
      (entries) => {
        const visible = entries
          .filter((e) => e.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        if (visible.length > 0) setActive(visible[0].target.id);
      },
      // The line at roughly 14% from the top of the viewport decides the "current" section, which
      // keeps a section near the bottom from stealing the highlight.
      { rootMargin: '-14% 0px -68% 0px', threshold: [0, 1] },
    );
    els.forEach((el) => obs.observe(el));
    return () => obs.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return active;
}

// One provider row while editing: name is the key it is referenced by, and an empty api_key
// keeps the stored value.
interface Row {
  name: string;
  type: string;
  base_url: string;
  default_model: string;
  api_key: string;   // newly entered; empty means leave unchanged
  has_api_key: boolean;
}

const TYPE_OPTIONS = [
  { value: 'openai', label: 'openai 兼容 (OpenAI / DeepSeek / Ollama / 网关)' },
  { value: 'anthropic', label: 'anthropic (Claude)' },
];

const PRESET_BASE_URL: Record<string, string> = {
  openai: 'https://api.openai.com/v1',
  anthropic: 'https://api.anthropic.com',
};

// LLMSettings maintains the list of providers the AI analysis nodes reference.
function LLMSettings() {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [err, setErr] = useState('');
  const [msg, setMsg] = useState('');
  const [saving, setSaving] = useState(false);

  const load = () => {
    api.getLLMProviders()
      .then((r) => {
        const list: Row[] = Object.entries(r.providers ?? {}).map(([name, p]) => ({
          name,
          type: p.type || 'openai',
          base_url: p.base_url || '',
          default_model: p.default_model || '',
          api_key: '',
          has_api_key: p.has_api_key,
        }));
        setRows(list);
      })
      .catch((e) => setErr(String(e.message || e)));
  };
  useEffect(load, []);

  const update = (i: number, patch: Partial<Row>) => {
    setRows((rs) => (rs ? rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)) : rs));
  };

  const addRow = () => {
    setRows((rs) => [...(rs ?? []), { name: '', type: 'openai', base_url: '', default_model: '', api_key: '', has_api_key: false }]);
  };

  const removeRow = (i: number) => {
    setRows((rs) => (rs ? rs.filter((_, idx) => idx !== i) : rs));
  };

  const save = async () => {
    setErr(''); setMsg('');
    if (!rows) return;
    const names = rows.map((r) => r.name.trim());
    if (names.some((n) => !n)) { setErr('每个 provider 都需要一个名称'); return; }
    if (new Set(names).size !== names.length) { setErr('provider 名称不能重复'); return; }

    const providers: Record<string, LLMProviderInput> = {};
    rows.forEach((r) => {
      providers[r.name.trim()] = {
        type: r.type,
        base_url: r.base_url.trim(),
        api_key: r.api_key,   // an empty string makes the backend keep the stored value
        default_model: r.default_model.trim(),
      };
    });

    setSaving(true);
    try {
      await api.setLLMProviders(providers);
      setMsg('已保存');
      load();
    } catch (e) {
      setErr(String((e as Error).message || e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section id="llm" className="settings-module">
      <div className="module-head">
        <span className="module-no">01</span>
        <div>
          <h2>大模型</h2>
          <p className="muted module-sub">
            AI 分析节点按这里配置的 provider 名引用模型，保存后即时生效。api_key 出于安全不回显，
            留空表示沿用已存值。
          </p>
        </div>
        <span className="module-actions">
          <button className="secondary" onClick={addRow}>新增 provider</button>
          <button disabled={saving} onClick={save}>{saving ? '保存中…' : '保存'}</button>
        </span>
      </div>

      {err && <div className="error">{err}</div>}
      {msg && <div className="muted" style={{ color: 'var(--up)' }}>{msg}</div>}

      {rows === null ? (
        <div className="empty">载入中…</div>
      ) : rows.length === 0 ? (
        <div className="empty">还没有配置任何模型 — 点「新增 provider」</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16, marginTop: 12 }}>
          {rows.map((r, i) => (
            <div key={i} className="provider-card">
              <div className="row" style={{ alignItems: 'flex-end' }}>
                <div>
                  <label>名称（节点中引用此名）</label>
                  <input value={r.name} placeholder="如 claude / deepseek" onChange={(e) => update(i, { name: e.target.value })} />
                </div>
                <div style={{ maxWidth: 280 }}>
                  <label>类型</label>
                  <Select
                    value={r.type}
                    onChange={(v) => update(i, { type: v, base_url: r.base_url || PRESET_BASE_URL[v] || '' })}
                    options={TYPE_OPTIONS}
                    style={{ width: '100%' }}
                  />
                </div>
              </div>
              <div className="row" style={{ alignItems: 'flex-end', marginTop: 10 }}>
                <div>
                  <label>Base URL</label>
                  <input
                    value={r.base_url}
                    placeholder={PRESET_BASE_URL[r.type] || ''}
                    onChange={(e) => update(i, { base_url: e.target.value })}
                  />
                </div>
                <div>
                  <label>默认模型</label>
                  <input
                    value={r.default_model}
                    placeholder={r.type === 'anthropic' ? 'claude-opus-4-8' : 'gpt-4o / deepseek-chat'}
                    onChange={(e) => update(i, { default_model: e.target.value })}
                  />
                </div>
              </div>
              <div className="row" style={{ alignItems: 'flex-end', marginTop: 10 }}>
                <div>
                  <label>API Key {r.has_api_key && <span className="muted">（已设置，留空保持不变）</span>}</label>
                  <input
                    type="password"
                    value={r.api_key}
                    placeholder={r.has_api_key ? '••••••••（已设置）' : '填入密钥'}
                    autoComplete="new-password"
                    onChange={(e) => update(i, { api_key: e.target.value })}
                  />
                </div>
                <div style={{ flex: '0 0 auto' }}>
                  <button className="danger small" onClick={() => removeRow(i)}>删除</button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function fmtTs(s?: string | null) {
  if (!s) return '从未';
  return s.slice(0, 19).replace('T', ' ');
}

// NewsSourceSettings maintains each news source's enabled flag and interval, and shows the last
// collection's status.
function NewsSourceSettings() {
  const [rows, setRows] = useState<NewsSourceStatus[] | null>(null);
  const [enabled, setEnabled] = useState(true);
  const [baseInterval, setBaseInterval] = useState(30);
  const [err, setErr] = useState('');
  const [msg, setMsg] = useState('');
  const [saving, setSaving] = useState(false);
  const [polling, setPolling] = useState<string>('');

  const load = () => {
    api.getNewsSources()
      .then((r) => {
        setRows(r.sources ?? []);
        setEnabled(r.enabled);
        setBaseInterval(r.base_interval_seconds);
      })
      .catch((e) => setErr(String(e.message || e)));
  };
  useEffect(load, []);

  const update = (i: number, patch: Partial<NewsSourceStatus>) => {
    setRows((rs) => (rs ? rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)) : rs));
  };

  const save = async () => {
    setErr(''); setMsg('');
    if (!rows) return;
    const cfgs: Record<string, NewsSourceSetting> = {};
    rows.forEach((r) => {
      cfgs[r.source] = { enabled: r.enabled, interval_seconds: r.interval_seconds };
    });
    setSaving(true);
    try {
      const r = await api.setNewsSources({ enabled, base_interval_seconds: baseInterval, sources: cfgs });
      setRows(r.sources ?? []);
      setEnabled(r.enabled);
      setBaseInterval(r.base_interval_seconds);
      setMsg('已保存');
    } catch (e) {
      setErr(String((e as Error).message || e));
    } finally {
      setSaving(false);
    }
  };

  const pollNow = async (source: string) => {
    setErr(''); setMsg(''); setPolling(source);
    try {
      const r = await api.pollNewsSource(source);
      setMsg(`${NEWS_SOURCE_LABEL[source] ?? source} 采集完成，新增 ${r.new} 条`);
      load();
    } catch (e) {
      setErr(String((e as Error).message || e));
    } finally {
      setPolling('');
    }
  };

  return (
    <section id="news" className="settings-module">
      <div className="module-head">
        <span className="module-no">02</span>
        <div>
          <h2>资讯数据源</h2>
          <p className="muted module-sub">
            配置已安装的数据源扩展。流水线可订阅扩展提供的资讯，也可通过事件接入接口接收你自己的数据。
          </p>
        </div>
        <span className="module-actions">
          <button disabled={saving || !rows?.length} onClick={save}>{saving ? '保存中…' : '保存'}</button>
        </span>
      </div>

      {err && <div className="error">{err}</div>}
      {msg && <div className="muted" style={{ color: 'var(--up)' }}>{msg}</div>}

      <div className="row" style={{ alignItems: 'center', gap: 24, marginTop: 12 }}>
        <label className="inline" style={{ gap: 8 }}>
          <input type="checkbox" disabled={!rows?.length} checked={enabled} onChange={(e) => setEnabled(e.target.checked)} style={{ width: 'auto' }} />
          <span>采集总开关</span>
        </label>
        <label className="inline" style={{ gap: 8 }}>
          <span>基准节拍(秒)</span>
          <input
            type="number"
            min={5}
            value={baseInterval}
            style={{ width: 90 }}
            onChange={(e) => setBaseInterval(Number(e.target.value))}
          />
        </label>
        <span className="muted">采集器每隔基准节拍醒来重读配置；关闭后仅空转重检，开启即恢复。</span>
      </div>

      {rows === null ? (
        <div className="empty">载入中…</div>
      ) : rows.length === 0 ? (
        <div className="empty">尚未安装资讯数据源。本项目保留扩展接口，不包含内置资讯采集器。</div>
      ) : (
        <table style={{ marginTop: 12 }}>
          <thead>
            <tr>
              <th>来源</th>
              <th>启用</th>
              <th>间隔(秒)</th>
              <th>上次采集</th>
              <th>上次新增</th>
              <th>状态</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={r.source}>
                <td>{NEWS_SOURCE_LABEL[r.source] ?? r.source}</td>
                <td>
                  <input
                    type="checkbox"
                    checked={r.enabled}
                    onChange={(e) => update(i, { enabled: e.target.checked })}
                  />
                </td>
                <td>
                  <input
                    type="number"
                    min={5}
                    value={r.interval_seconds}
                    style={{ width: 80 }}
                    onChange={(e) => update(i, { interval_seconds: Number(e.target.value) })}
                  />
                </td>
                <td className="mono muted">{fmtTs(r.last_polled_at)}</td>
                <td className="mono">{r.last_fetched ? `${r.last_new}/${r.last_fetched}` : '—'}</td>
                <td>{r.last_error ? <span className="error">{r.last_error}</span> : <span className="muted">正常</span>}</td>
                <td>
                  <button className="secondary small" disabled={polling === r.source} onClick={() => pollNow(r.source)}>
                    {polling === r.source ? '采集中…' : '立即采集'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

// NotifySettings maintains the delivery nodes' outbound channel config: email over SMTP and the
// Feishu webhook. Delivery nodes use the channels configured here to send messages and AI fields
// to the outside world. Credentials are never echoed back, for safety, and leaving one blank
// keeps it unchanged.
function NotifySettings() {
  return (
    <section id="notify" className="settings-module">
      <div className="module-head">
        <span className="module-no">03</span>
        <div>
          <h2>触达通道</h2>
          <p className="muted module-sub">
            流水线的「触达」节点按这里配置的渠道把消息与 AI 生成字段送达外界。密钥出于安全不回显，
            留空表示沿用已存值，保存后即时生效。
          </p>
        </div>
      </div>
      <EmailSettings />
      <FeishuSettings />
    </section>
  );
}

// EmailSettings maintains the email (SMTP) channel config.
function EmailSettings() {
  const [host, setHost] = useState('');
  const [port, setPort] = useState(465);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [from, setFrom] = useState('');
  const [useSSL, setUseSSL] = useState(true);
  const [defaultTo, setDefaultTo] = useState('');
  const [hasPassword, setHasPassword] = useState(false);
  const [err, setErr] = useState('');
  const [msg, setMsg] = useState('');
  const [saving, setSaving] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const load = () => {
    api.getNotifyEmail()
      .then((r) => {
        const e = r.email;
        setHost(e.host || '');
        setPort(e.port || 465);
        setUsername(e.username || '');
        setFrom(e.from || '');
        setUseSSL(e.use_ssl);
        setDefaultTo((e.default_to ?? []).join('\n'));
        setHasPassword(e.has_password);
        setLoaded(true);
      })
      .catch((e) => setErr(String(e.message || e)));
  };
  useEffect(load, []);

  const save = async () => {
    setErr(''); setMsg('');
    const body: EmailSettingInput = {
      host: host.trim(),
      port: Number(port) || 0,
      username: username.trim(),
      password,            // an empty string makes the backend keep the stored value
      from: from.trim(),
      use_ssl: useSSL,
      default_to: defaultTo.split('\n').map((s) => s.trim()).filter((s) => s.length > 0),
    };
    setSaving(true);
    try {
      const r = await api.setNotifyEmail(body);
      setHasPassword(r.email.has_password);
      setPassword('');
      setMsg('已保存');
    } catch (e) {
      setErr(String((e as Error).message || e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="provider-card" style={{ marginTop: 12 }}>
      <div className="row" style={{ alignItems: 'center', justifyContent: 'space-between' }}>
        <strong>邮箱 (SMTP)</strong>
        <button disabled={saving || !loaded} onClick={save}>{saving ? '保存中…' : '保存'}</button>
      </div>
      {err && <div className="error">{err}</div>}
      {msg && <div className="muted" style={{ color: 'var(--up)' }}>{msg}</div>}

      <div className="row" style={{ alignItems: 'flex-end', marginTop: 10 }}>
        <div style={{ flex: 2 }}>
          <label>SMTP 服务器</label>
          <input value={host} placeholder="如 smtp.feishu.cn / smtp.gmail.com" onChange={(e) => setHost(e.target.value)} />
        </div>
        <div style={{ flex: 1 }}>
          <label>端口</label>
          <input type="number" value={port} onChange={(e) => setPort(Number(e.target.value))} />
        </div>
        <div style={{ flex: '0 0 auto' }}>
          <label className="inline" style={{ gap: 8, cursor: 'pointer' }}>
            <input type="checkbox" checked={useSSL} onChange={(e) => setUseSSL(e.target.checked)} style={{ width: 'auto' }} />
            <span>SSL(465)</span>
          </label>
        </div>
      </div>

      <div className="row" style={{ alignItems: 'flex-end', marginTop: 10 }}>
        <div>
          <label>用户名</label>
          <input value={username} placeholder="登录账号(通常为完整邮箱)" onChange={(e) => setUsername(e.target.value)} />
        </div>
        <div>
          <label>密码 / 授权码 {hasPassword && <span className="muted">（已设置，留空保持不变）</span>}</label>
          <input
            type="password"
            value={password}
            placeholder={hasPassword ? '••••••••（已设置）' : '填入密码或授权码'}
            autoComplete="new-password"
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
      </div>

      <div className="row" style={{ alignItems: 'flex-end', marginTop: 10 }}>
        <div>
          <label>发件人 (留空用用户名)</label>
          <input value={from} placeholder="如 bot@your.com" onChange={(e) => setFrom(e.target.value)} />
        </div>
        <div>
          <label>默认收件人 (每行一个)</label>
          <textarea rows={2} value={defaultTo} placeholder="节点未填收件人时用此列表，每行一个" onChange={(e) => setDefaultTo(e.target.value)} />
        </div>
      </div>
    </div>
  );
}

// FeishuSettings maintains the Feishu custom bot webhook config.
function FeishuSettings() {
  const [webhook, setWebhook] = useState('');
  const [secret, setSecret] = useState('');
  const [hasSecret, setHasSecret] = useState(false);
  const [err, setErr] = useState('');
  const [msg, setMsg] = useState('');
  const [saving, setSaving] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const load = () => {
    api.getNotifyFeishu()
      .then((r) => {
        setWebhook(r.feishu.webhook_url || '');
        setHasSecret(r.feishu.has_secret);
        setLoaded(true);
      })
      .catch((e) => setErr(String(e.message || e)));
  };
  useEffect(load, []);

  const save = async () => {
    setErr(''); setMsg('');
    const body: FeishuSettingInput = { webhook_url: webhook.trim(), secret };
    setSaving(true);
    try {
      const r = await api.setNotifyFeishu(body);
      setHasSecret(r.feishu.has_secret);
      setSecret('');
      setMsg('已保存');
    } catch (e) {
      setErr(String((e as Error).message || e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="provider-card" style={{ marginTop: 16 }}>
      <div className="row" style={{ alignItems: 'center', justifyContent: 'space-between' }}>
        <strong>飞书 (自定义机器人)</strong>
        <button disabled={saving || !loaded} onClick={save}>{saving ? '保存中…' : '保存'}</button>
      </div>
      {err && <div className="error">{err}</div>}
      {msg && <div className="muted" style={{ color: 'var(--up)' }}>{msg}</div>}

      <div className="row" style={{ alignItems: 'flex-end', marginTop: 10 }}>
        <div style={{ flex: 1 }}>
          <label>Webhook 地址</label>
          <input value={webhook} placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..." onChange={(e) => setWebhook(e.target.value)} />
        </div>
      </div>
      <div className="row" style={{ alignItems: 'flex-end', marginTop: 10 }}>
        <div style={{ flex: 1 }}>
          <label>签名密钥 (可选) {hasSecret && <span className="muted">（已设置，留空保持不变）</span>}</label>
          <input
            type="password"
            value={secret}
            placeholder={hasSecret ? '••••••••（已设置）' : '机器人开启签名校验时填写'}
            autoComplete="new-password"
            onChange={(e) => setSecret(e.target.value)}
          />
        </div>
      </div>
    </div>
  );
}
