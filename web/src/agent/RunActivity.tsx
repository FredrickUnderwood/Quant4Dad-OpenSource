import { useEffect, useState } from 'react';

export function RunActivity({ label, running }: { label: string; running: boolean }) {
  const [started] = useState(Date.now), [seconds, setSeconds] = useState(0);
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => setSeconds(Math.floor((Date.now() - started) / 1000)), 1000);
    return () => clearInterval(timer);
  }, [running, started]);
  return <div className="assistant-activity" data-running={running}>
    <span className="assistant-activity-mark" aria-hidden="true">{running ? <><i /><i /><i /></> : '·'}</span>
    <span role="status">{label}</span>
    {running && <span className="assistant-activity-time" aria-label={`已等待 ${seconds} 秒`}>{seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`}</span>}
  </div>;
}
