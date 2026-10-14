// Local development only. Restarts the API and leaves clearly marked test records.
import { request } from '../frontend/node_modules/@playwright/test/index.mjs'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'

const root = new URL('../', import.meta.url)
const credentials = JSON.parse(readFileSync(new URL('.env.admin.json', root), 'utf8'))
const origin = 'http://127.0.0.1:8088'
const hr = await request.newContext({ baseURL: origin, extraHTTPHeaders: { Origin: origin } })
const candidate = await request.newContext({ baseURL: origin, extraHTTPHeaders: { Origin: origin } })
async function checked(client, method, path, body, expected = 200, csrf) {
  const response = await client.fetch('/api/v1' + path, { method, data: body, headers: csrf ? { 'X-CSRF-Token': csrf } : {} })
  assert.equal(response.status(), expected, 'Unexpected HTTP status')
  return expected === 204 ? undefined : response.json()
}
let invitationID, csrf, primaryFailure
try {
  const user = await checked(hr, 'POST', '/auth/login', { email: credentials.email, password: credentials.password })
  csrf = user.csrf_token
  const marker = 'Runtime restart check ' + Date.now()
  const question = await checked(hr, 'POST', '/questions', { body: marker, type: 'single_choice', difficulty: 1, options: [{ label: 'Correct', is_correct: true }, { label: 'Wrong', is_correct: false }] }, 201, csrf)
  const source = await checked(hr, 'GET', '/questions/' + question.id)
  await checked(hr, 'POST', '/questions/' + question.id + '/publish', { base_version_no: 1 }, 200, csrf)
  const test = await checked(hr, 'POST', '/tests', { title: marker, duration_seconds: 120, pass_percent: '70.00', show_candidate_score: true, shuffle_questions: true, shuffle_options: true, items: [{ question_version_id: source.version_id, points: '2.50' }] }, 201, csrf)
  await checked(hr, 'POST', '/tests/' + test.id + '/publish', { base_version_no: 1 }, 200, csrf)
  const version = await checked(hr, 'GET', '/tests/' + test.id)
  const person = await checked(hr, 'POST', '/candidates', { full_name: marker }, 201, csrf)
  const issued = await checked(hr, 'POST', '/invitations', { candidate_id: person.id, test_version_id: version.version_id, max_attempts: 1, expires_at: new Date(Date.now() + 25000).toISOString() }, 201, csrf)
  invitationID = issued.invitation.id
  const prefix = '/invite/' + issued.path.slice('/i/'.length)
  const start = await checked(candidate, 'POST', prefix + '/start', { request_id: randomUUID(), consent: true })
  const item = start.items[0], selected = source.options.find(option => option.is_correct).id
  await checked(candidate, 'PUT', prefix + '/attempts/' + start.id + '/items/' + item.id + '/answer', { request_id: randomUUID(), expected_revision: 0, selected_option_ids: [selected] })
  try {
    execFileSync('docker', ['compose', 'stop', 'api'], { cwd: root, stdio: 'pipe', timeout: 30000 })
    const offline = await candidate.get('/api/v1' + prefix + '/attempt', { timeout: 5000 })
    assert.ok([502,504].includes(offline.status()), 'Nginx should report unavailable upstream')
  } finally {
    execFileSync('docker', ['compose', 'restart', 'api'], { cwd: root, stdio: 'pipe', timeout: 30000 })
  }
  let ready = false
  for (let n = 0; n < 80; n++) {
    try { const response = await hr.get('/api/v1/status'); if (response.status() === 200) { ready = true; break } } catch {}
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  assert.ok(ready, 'API readiness did not recover after restart')
  let restored
  for (let n = 0; n < 40; n++) {
    try { restored = await checked(candidate, 'GET', prefix + '/attempt'); break } catch { await new Promise(resolve => setTimeout(resolve, 100)) }
  }
  assert.ok(restored, 'API did not become available after restart')
  assert.equal(restored.id, start.id)
  assert.equal(restored.deadline_at, start.deadline_at)
  assert.equal(restored.status, 'in_progress', 'Restart must complete before the test deadline')
  assert.deepEqual(restored.items[0].options, item.options)
  assert.deepEqual(restored.items[0].selected_option_ids, [selected])
  const logs = execFileSync('docker', ['compose', 'logs', '--no-color', 'web'], { cwd: root, encoding: 'utf8' })
  assert.equal(logs.includes(issued.path.slice('/i/'.length)), false, 'Invitation secret leaked in Nginx logs')
  // Do not call a candidate endpoint here: only the running background worker can expire it.
  let result
  for (let n = 0; n < 120; n++) {
    const results = await checked(hr, 'GET', '/results')
    result = results.items.find(row => row.id === start.id)
    if (result?.status === 'expired') break
    await new Promise(resolve => setTimeout(resolve, 250))
  }
  assert.equal(result?.status, 'expired', 'Background worker did not complete a browserless attempt')
  assert.equal(result.score_points, '2.50')
  assert.equal(result.score_percent, '100.00')
  const finished = await checked(candidate, 'GET', prefix + '/attempt')
  assert.equal(finished.score.percent, '100.00')
  await checked(candidate, 'PUT', prefix + '/attempts/' + start.id + '/items/' + item.id + '/answer', { request_id: randomUUID(), expected_revision: 1, selected_option_ids: [] }, 409)
  console.log('PASS: API restart preserves attempt, deadline, option order and confirmed answers; background expiry stores the correct frozen score.')
} catch (error) {
  primaryFailure = error
  throw error
} finally {
  if (invitationID && csrf) {
    try { await checked(hr, 'POST', '/invitations/' + invitationID + '/revoke', undefined, 204, csrf) }
    catch (error) { if (!primaryFailure) throw error; console.error('Fixture invitation cleanup did not complete.') }
  }
  await hr.dispose(); await candidate.dispose()
}
