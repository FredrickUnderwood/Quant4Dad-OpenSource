import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { api } from '../api';

// The login page always navigates through window.location, which reinitializes React state
// completely and sidesteps the cached auth state in App that would otherwise bounce a
// just-logged-in user straight back to the login page.
function goTo(path: string) { window.location.replace(path); }

export default function LoginPage() {
  const [token, setToken] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const [sp] = useSearchParams();
  const from = sp.get('from') || '/strategies';

  // Already logged in, or the backend has auth disabled: navigate away rather than idle here.
  useEffect(() => {
    api.authStatus().then(s => {
      if (!s.auth_required || s.authenticated) goTo(from);
    }).catch(() => {});
  }, [from]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr('');
    setBusy(true);
    try {
      await api.login(token);
      goTo(from);
    } catch (e: any) {
      setErr(e.message === 'unauthorized' ? '令牌错误' : (e.message || '登录失败'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-shell">
      <div className="login-card">
        <h2>Quant4Dad</h2>
        <p className="login-sub">access · token required</p>
        <form onSubmit={submit}>
          <label>访问令牌</label>
          <input
            type="password" autoFocus value={token}
            onChange={e => setToken(e.target.value)}
            style={{ marginBottom: 18 }}
            placeholder="config.yaml 中的 security.token"
          />
          {err && <p className="error" style={{ marginBottom: 12 }}>{err}</p>}
          <button type="submit" disabled={busy || !token}>
            {busy ? '登录中…' : '进入实验室 →'}
          </button>
        </form>
        <div className="foot">est. for dad · v0.1</div>
      </div>
    </div>
  );
}
