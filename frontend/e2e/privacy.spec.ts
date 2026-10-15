import { expect, test } from '@playwright/test'
import { readFileSync } from 'node:fs'
const credentials = JSON.parse(readFileSync(process.env.E2E_CREDENTIALS_FILE || new URL('../../.env.admin.json', import.meta.url), 'utf8'))

test('candidate erasure: preview, changed data, explicit confirmation and revoked access', async ({ page }, info) => {
  await page.goto('/login')
  await page.getByLabel('Электронная почта').fill(credentials.email)
  await page.getByLabel('Пароль', { exact: true }).fill(credentials.password)
  await page.getByRole('button', { name: 'Войти', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Всё начинается с людей.' })).toBeVisible()
  const me = await (await page.request.get('/api/v1/me')).json()
  const headers = { Origin: new URL(page.url()).origin, 'X-CSRF-Token': me.csrf_token }
  async function call(method: string, path: string, data?: unknown, expected = 200) {
    const response = await page.request.fetch('/api/v1' + path, { method, data, headers })
    expect(response.status()).toBe(expected)
    return response.status() === 204 ? undefined : response.json()
  }
  const marker = 'E2E удаление ' + info.project.name + ' ' + Date.now()
  const candidate = await call('POST', '/candidates', { full_name: marker, email: 'synthetic-privacy@example.test' }, 201)
  let testID: string | undefined, questionID: string | undefined, deleted = false
  try {
    const question = await call('POST', '/questions', { body: marker, type: 'single_choice', difficulty: 1, options: [{ label: 'A', is_correct: true }, { label: 'B', is_correct: false }] }, 201)
    questionID = question.id
    await call('POST', '/questions/' + question.id + '/publish', { base_version_no: 1 })
    const source = await call('GET', '/questions/' + question.id)
    const record = await call('POST', '/tests', { title: marker, duration_seconds: 120, pass_percent: '70.00', show_candidate_score: false, shuffle_questions: false, shuffle_options: false, items: [{ question_version_id: source.version_id, points: '1.00' }] }, 201)
    testID = record.id
    await call('POST', '/tests/' + record.id + '/publish', { base_version_no: 1 })
    const version = await call('GET', '/tests/' + record.id)
    const invitation = await call('POST', '/invitations', { candidate_id: candidate.id, test_version_id: version.version_id, expires_at: new Date(Date.now() + 3600000).toISOString() }, 201)
    const prefix = '/invite/' + invitation.path.split('/').pop()
    const landing = await call('GET', prefix)
    await call('POST', prefix + '/start', { request_id: crypto.randomUUID(), consent: true, consent_revision: landing.consent_document.revision })
    await page.goto('/candidates/' + candidate.id)
    await page.getByRole('button', { name: 'Подготовить полное удаление', exact: true }).click()
    await expect(page.getByText('Активных прохождений: 1. Их данные также будут удалены.')).toBeVisible()
    const remove = page.getByRole('button', { name: 'Удалить кандидата и результаты', exact: true })
    await expect(remove).toBeDisabled()
    await call('PATCH', '/candidates/' + candidate.id, { full_name: marker + ' изменён', email: 'synthetic-privacy@example.test' })
    await page.getByLabel('Подтверждение полного удаления').fill('УДАЛИТЬ КАНДИДАТА')
    await remove.click()
    await expect(page.getByRole('alert')).toContainText('Данные изменились')
    await page.getByRole('button', { name: 'Подготовить полное удаление', exact: true }).click()
    await expect(page.getByRole('heading', { name: marker + ' изменён', exact: true })).toBeVisible()
    await page.getByLabel('Подтверждение полного удаления').fill('УДАЛИТЬ КАНДИДАТА')
    await remove.click()
    await expect(page).toHaveURL(/\/candidates$/)
    await call('GET', '/candidates/' + candidate.id, undefined, 404)
    await call('GET', prefix + '/attempt', undefined, 404)
    deleted = true
    await expect(page.getByRole('heading', { name: 'Кандидаты', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  } finally {
    // Only this synthetic candidate and this test's bank entries are touched.
    if (!deleted) {
      const preview = await call('POST', '/candidates/' + candidate.id + '/erasure/preview', {})
      await call('POST', '/erasure/previews/' + preview.id + '/apply', { confirmation: 'УДАЛИТЬ КАНДИДАТА' })
    }
    if (testID) await call('POST', '/tests/' + testID + '/archive', {}, 204)
    if (questionID) await call('POST', '/questions/' + questionID + '/archive', {}, 204)
  }
})
