import { expect, test } from '@playwright/test'
import { readFileSync } from 'node:fs'
const credentials = JSON.parse(readFileSync(new URL('../../.env.admin.json', import.meta.url), 'utf8'))
const fixture = JSON.parse(readFileSync(new URL('../../backend/internal/httpapi/testdata/candidate-tester-v1.json', import.meta.url), 'utf8'))
test('JSON import: preview, explicit apply and duplicate detection', async ({ page }, info) => {
  const bank = structuredClone(fixture), suffix = '_' + info.project.name + '_' + Date.now()
  bank.questions.forEach((q: { id: string; text: string }) => { q.id += suffix; q.text = 'E2E импорт ' + q.text })
  bank.tests.forEach((t: { id: string; title: string; questionIds: string[] }) => { t.id += suffix; t.title = 'E2E ' + t.title; t.questionIds = t.questionIds.map(id => id + suffix) })
  await page.goto('/questions/import')
  await page.getByLabel('Электронная почта').fill(credentials.email)
  await page.getByLabel('Пароль', { exact: true }).fill(credentials.password)
  await page.getByRole('button', { name: 'Войти', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Всё начинается с людей.' })).toBeVisible()
  await page.goto('/questions')
  await page.getByRole('link', { name: 'Импорт JSON', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Импорт CandidateTester' })).toBeVisible()
  const file = { name: 'fixture-import.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(bank)) }
  await page.getByLabel('JSON-файл', { exact: true }).setInputFiles(file)
  await page.getByRole('button', { name: 'Проверить файл', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Предварительный отчёт' })).toContainText('Вопросов: 2')
  await expect(page.getByText('Баллы скрыты от кандидата', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Импортировать в организацию', exact: true })).toBeDisabled()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.getByLabel('Введите ИМПОРТИРОВАТЬ', { exact: true }).fill('ИМПОРТИРОВАТЬ')
  const response = page.waitForResponse(r => r.url().endsWith('/api/v1/import/questions/apply') && r.request().method() === 'POST')
  await page.getByRole('button', { name: 'Импортировать в организацию', exact: true }).click()
  const applied = await response; expect(applied.status()).toBe(201)
  const receipt = await applied.json()
  try {
    await expect(page.getByRole('heading', { name: 'Импорт завершён', exact: true })).toBeVisible()
    await expect(page.getByRole('status')).toContainText('Добавлено вопросов: 2, тестов: 2')
    await page.reload()
    await page.getByLabel('JSON-файл', { exact: true }).setInputFiles(file)
    await page.getByRole('button', { name: 'Проверить файл', exact: true }).click()
    await expect(page.getByRole('status')).toContainText('уже импортирован')
    await expect(page.getByRole('button', { name: 'Импортировать в организацию', exact: true })).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  } finally {
    // Archive only this scenario's objects; keep immutable history and import receipt.
    const me = await (await page.request.get('/api/v1/me')).json()
    for (const [kind, objects] of [['tests', receipt.tests], ['questions', receipt.questions]] as const) {
      for (const object of objects) {
        const result = await page.request.post(`/api/v1/${kind}/${object.id}/archive`, { data: {}, headers: { Origin: new URL(page.url()).origin, 'X-CSRF-Token': me.csrf_token } })
        expect(result.status()).toBe(204)
      }
    }
  }
})
