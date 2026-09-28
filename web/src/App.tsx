import { useEffect, useState } from 'react';
import { Link, NavLink, Route, Routes, Navigate, useLocation } from 'react-router-dom';
import StrategiesPage from './pages/StrategiesPage';
import StrategyEditor from './pages/StrategyEditor';
import DataSyncPage from './pages/DataSyncPage';
import BacktestsPage from './pages/BacktestsPage';
import BacktestDetailPage from './pages/BacktestDetailPage';
import BacktestLaunchPage from './pages/BacktestLaunchPage';
import PipelinesPage from './pages/PipelinesPage';
import PipelineEditor from './pages/PipelineEditor';
import EventsPage from './pages/EventsPage';
import NewsPage from './pages/NewsPage';
import SettingsPage from './pages/SettingsPage';
import LoginPage from './pages/LoginPage';
import ThemeToggle from './components/ThemeToggle';
import AssistantPage from './pages/AssistantPage';
import { agentAPI } from './agent/api';
import { api } from './api';

type AuthState = 'loading' | 'authed' | 'guest';

export default function App() {
  const loc = useLocation();
  const isLogin = loc.pathname === '/login';
  const [auth, setAuth] = useState<AuthState>('loading');
  const [assistant, setAssistant] = useState(false);
  useEffect(() => {
    if (auth !== 'authed' || isLogin) { setAssistant(false); return; }
    const abort = new AbortController();
    agentAPI.options(abort.signal).then(() => { if (!abort.signal.aborted) setAssistant(true); }).catch(() => { if (!abort.signal.aborted) setAssistant(false); });
    return () => abort.abort();
  }, [auth, isLogin]);

  // Ask the backend about the login state once before entering any protected page, rather than
  // waiting for some API call to throw a 401. That avoids the flash of content, and the missed
  // redirect when an error gets swallowed.
  useEffect(() => {
    if (isLogin) return;
    let cancelled = false;
    api.authStatus()
      .then(s => {
        if (cancelled) return;
        if (!s.auth_required || s.authenticated) {
          setAuth('authed');
        } else {
          setAuth('guest');
        }
      })
      .catch(() => { if (!cancelled) setAuth('guest'); });
    return () => { cancelled = true; };
  }, [isLogin]);

  // Log out with a hard navigation, which throws away all React state, so no cached state can
  // fire off another protected request.
  const onLogout = async () => {
    try { await api.logout(); } catch {}
    window.location.replace('/login');
  };

  if (isLogin) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
      </Routes>
    );
  }

  if (auth === 'loading') {
    return <div className="empty">校准市场时钟…</div>;
  }

  if (auth === 'guest') {
    const from = encodeURIComponent(loc.pathname + loc.search);
    return <Navigate to={`/login?from=${from}`} replace />;
  }

  return (
    <>
      <header>
        <h1>
          <span className="brand-mark">Q4D</span>
          Quant4Dad
        </h1>
        <nav>
          <NavLink to="/strategies">策略</NavLink>
          <NavLink to="/datasync">数据同步</NavLink>
          <NavLink to="/backtests">回测</NavLink>
          <NavLink to="/pipelines">流水线</NavLink>
          <NavLink to="/events">事件</NavLink>
          <NavLink to="/news">资讯</NavLink>
          {assistant && <NavLink to="/assistant">助手</NavLink>}
          <NavLink to="/settings">设置</NavLink>
        </nav>
        <span className="live-dot">Live · CN/A</span>
        <span className="inline" style={{ gap: 10 }}>
          <ThemeToggle />
          <button className="ghost" onClick={onLogout}>退出</button>
        </span>
      </header>
      <main className={loc.pathname === '/assistant' ? 'assistant-layout' : undefined}>
        <Routes>
          <Route path="/" element={<Navigate to="/strategies" replace />} />
          <Route path="/strategies" element={<StrategiesPage />} />
          <Route path="/strategies/new" element={<StrategyEditor />} />
          <Route path="/strategies/:id/edit" element={<StrategyEditor />} />
          <Route path="/datasync" element={<DataSyncPage />} />
          <Route path="/strategies/:id/backtest" element={<BacktestLaunchPage />} />
          <Route path="/backtests" element={<BacktestsPage />} />
          <Route path="/backtests/:id" element={<BacktestDetailPage />} />
          <Route path="/pipelines" element={<PipelinesPage />} />
          <Route path="/pipelines/new" element={<PipelineEditor />} />
          <Route path="/pipelines/:id/edit" element={<PipelineEditor />} />
          <Route path="/events" element={<EventsPage />} />
          <Route path="/news" element={<NewsPage />} />
          <Route path="/assistant" element={<AssistantPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="*" element={<div className="empty">页面不存在 — <Link to="/strategies">返回</Link></div>} />
        </Routes>
      </main>
    </>
  );
}
