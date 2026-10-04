// A stand-in for the Coolify API, for testing ../coolify-deploy.sh. Usage: node fake-coolify.mjs <port> <mode>
import http from 'node:http'
const [port, mode] = [Number(process.argv[2]), process.argv[3]]
let polls = 0
const requests = []
http.createServer((req, res) => {
  const url = new URL(req.url, 'http://x')
  requests.push(req.method + ' ' + url.pathname + url.search + ' auth=' + (req.headers.authorization || ''))
  const json = (code, body) => { res.writeHead(code, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(body)) }
  if (url.pathname === '/__requests') return json(200, requests)
  if (req.headers.authorization !== 'Bearer secret-token') return json(401, { message: 'Unauthenticated.' })
  if (url.pathname === '/api/v1/deploy') {
    if (mode === 'get-only' && req.method === 'POST') return json(405, { message: 'Method not allowed' })
    if (mode === 'http500') return json(500, { message: 'boom' })
    if (mode === 'no-id') return json(200, { deployments: [] })
    if (url.searchParams.get('uuid') !== 'app-uuid') return json(404, { message: 'not found' })
    return json(200, { deployments: [{ message: 'queued', resource_uuid: 'app-uuid', deployment_uuid: 'dep-1' }] })
  }
  if (url.pathname === '/api/v1/deployments/dep-1') {
    polls++
    if (mode === 'fail') return json(200, { status: polls < 2 ? 'in_progress' : 'failed' })
    if (mode === 'slow') return json(200, { status: 'in_progress' })
    if (mode === 'cancelled') return json(200, { status: 'cancelled-by-user' })
    return json(200, { status: polls < 3 ? (polls < 2 ? 'queued' : 'in_progress') : 'finished' })
  }
  json(404, { message: 'not found' })
}).listen(port, '127.0.0.1')
