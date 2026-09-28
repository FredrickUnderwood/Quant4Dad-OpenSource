export class BridgeError extends Error {
  constructor(code, status) { super(code); this.status = status }
}

export function sendJSON(res, status, body, headers = {}) {
  if (res.destroyed || res.writableEnded) return
  res.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store', ...headers })
  res.end(JSON.stringify(body))
}
