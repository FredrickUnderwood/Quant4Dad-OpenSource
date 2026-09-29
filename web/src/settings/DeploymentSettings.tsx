import { useEffect, useState } from 'react';
import { api } from '../api';
import { SETTINGS_DOCS, SettingsHead, settingsError } from './IntegrationSettings';
import type { DeploymentView } from './types';

export function useDeploymentSettings() {
  const [view, setView] = useState<DeploymentView | null>(null), [error, setError] = useState(''), [busy, setBusy] = useState(false);
  const reload = async () => { setBusy(true); setError(''); try { setView(await api.getDeployment()); } catch (e) { setError(settingsError(e)); } finally { setBusy(false); } };
  useEffect(() => { void reload(); }, []);
  return { view, error, busy, reload };
}
export function DeploymentSettings({ state }: { state: ReturnType<typeof useDeploymentSettings> }) {
  const { view, error, busy, reload } = state;
  return <section id="deployment" className="settings-module">
    <SettingsHead number="06" title="安全与部署">这些配置决定进程连接、存储位置与认证，页面只展示脱敏状态和修改指引。</SettingsHead>
    {view && <>
      <div className="settings-status"><span>数据库：{view.storage_backend}</span><span>服务时区：{view.timezone}</span><span>登录认证：{view.authentication_enabled ? '已启用' : '未启用'}</span><span>后台任务：{view.background_enabled ? '已启用' : '已关闭'}</span><span>Agent：{view.agent_enabled ? '已启用' : '未启用'}</span><span>MCP：{view.mcp_enabled ? '已启用' : '未启用'}</span></div>
      <div className="settings-table-scroll"><table><thead><tr><th>配置项</th><th>当前值</th><th>修改方式</th></tr></thead><tbody>{view.items.map(item => <tr key={item.key}><td>{item.label}</td><td className="settings-config-value">{item.value || '—'}</td><td>{item.change_hint}</td></tr>)}</tbody></table></div>
    </>}
    {error && <p className="error" role="alert">{error}</p>}{!view && !error && <p>正在读取部署状态…</p>}
    <div className="settings-actions"><button className="secondary" disabled={busy} onClick={() => void reload()}>刷新部署状态</button><a href={`${SETTINGS_DOCS}deployment.md`} target="_blank" rel="noreferrer">安装和部署说明</a><a href={`${SETTINGS_DOCS}settings.md`} target="_blank" rel="noreferrer">完整配置清单</a></div>
    <details className="settings-details"><summary>哪些配置仍需通过安装目录修改？</summary><ul>
      <li>数据库类型与 DSN、数据目录、绑定地址、端口、服务地址、可信代理与服务时区。</li>
      <li>使用安装器的 <code>--with-agent</code> / <code>--with-mcp</code> 部署可选服务；启用 Agent 后在本页配置模型。</li>
      <li>登录 token 和 MCP token 是不同凭据。页面不显示或轮换这些值，默认文件位于安装状态目录的 <code>config/login-token</code> 和 <code>config/mcp-token</code>；自定义状态目录时路径相应变化。</li>
      <li>MCP token 授权全部业务工具及写操作，不能代替登录 token。Agent 内部控制 token、签名密钥和 profile 配置由安装器维护。</li>
      <li>凭据轮换需同步更新对应服务配置并重启；MCP 代理和 API 必须使用一致的新 MCP token。不要把凭据放进截图、日志或仓库。</li>
    </ul></details>
  </section>;
}
