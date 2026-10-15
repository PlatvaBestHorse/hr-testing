import { test, expect } from '@playwright/test'
import { readFileSync, readdirSync } from 'node:fs'
import { randomBytes, createHmac } from 'node:crypto'
const credentials = JSON.parse(readFileSync(process.env.E2E_CREDENTIALS_FILE || new URL('../../.env.admin.json', import.meta.url), 'utf8'))
const mailbox = new URL('../../.mailbox/', import.meta.url)
function readLink(email: string, previous: Set<string>) {
  for (const file of readdirSync(mailbox)) {
    if (previous.has(file) || !file.endsWith('.eml')) continue
    const message = readFileSync(new URL(file, mailbox), 'utf8')
    if (!message.includes('To: ' + email + '\r\n') && !message.includes('To: <' + email + '>\r\n')) continue
    const link = message.split(/\r?\n/).find(line => line.includes('/reset-password#token='))
    if (link) return link
  }
  return ''
}
function totp(secret: string) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567', decoded: number[] = []
  let bits = 0, value = 0
  for (const char of secret) { value = (value << 5) | alphabet.indexOf(char); bits += 5; if (bits >= 8) { decoded.push((value >>> (bits - 8)) & 255); bits -= 8 } }
  const counter = Buffer.alloc(8); counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)))
  const hash = createHmac('sha1', Buffer.from(decoded)).update(counter).digest(), offset = hash[19]! & 15
  return ((hash.readUInt32BE(offset) & 0x7fffffff) % 1000000).toString().padStart(6, '0')
}
test('account: local invitation, password change, MFA and recovery', async ({ page, browser }, info) => {
  const options = await (await page.request.get('/api/v1/auth/options')).json()
  test.skip(!options.local_mail, 'This scenario must use the private local mailbox, never external SMTP')
  const email = 'security-' + info.project.name + '-' + randomBytes(6).toString('hex') + '@local.test'
  const initialPassword = randomBytes(24).toString('hex'), changedPassword = randomBytes(24).toString('hex'), recoveredPassword = randomBytes(24).toString('hex')
  await page.goto('/login'); await page.getByLabel('Электронная почта').fill(credentials.email); await page.getByLabel('Пароль', { exact: true }).fill(credentials.password); await page.getByRole('button', { name: 'Войти', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Всё начинается с людей.' })).toBeVisible()
  const me = await (await page.request.get('/api/v1/me')).json()
  const previous = new Set(readdirSync(mailbox))
  const staff = await browser.newContext({ viewport: info.project.use.viewport, baseURL: String(info.project.use.baseURL || 'http://127.0.0.1:8088') })
  let userID = ''
  try {
    await page.goto('/team'); await page.getByLabel('Имя сотрудника', { exact: true }).fill('Проверка безопасности ' + info.project.name); await page.getByLabel('Почта сотрудника', { exact: true }).fill(email)
    await page.getByRole('button', { name: 'Добавить сотрудника', exact: true }).click()
    await expect(page.getByRole('status')).toContainText('Приглашение подготовлено')
    const team = await (await page.request.get('/api/v1/team')).json(); userID = team.items.find((member: { email: string }) => member.email === email).id
    await expect.poll(() => Boolean(readLink(email, previous))).toBe(true)
    const account = await staff.newPage(); await account.goto(readLink(email, previous))
    await expect(account).toHaveURL(/\/reset-password$/)
    await account.getByLabel('Новый пароль', { exact: true }).fill(initialPassword); await account.getByLabel('Повтор нового пароля').fill(initialPassword); await account.getByRole('button', { name: 'Сохранить новый пароль', exact: true }).click()
    await expect(account.getByRole('status')).toContainText('Пароль изменён')
    await account.getByRole('link', { name: 'Вернуться ко входу' }).click()
    const signIn = async (password: string, code?: string) => {
      await account.getByLabel('Электронная почта').fill(email); await account.getByLabel('Пароль', { exact: true }).fill(password); await account.getByRole('button', { name: 'Войти', exact: true }).click()
      if (code) { await account.getByLabel('Код приложения или резервный код').fill(code); await account.getByRole('button', { name: 'Войти', exact: true }).click() }
      await expect(account.getByRole('heading', { name: 'Всё начинается с людей.' })).toBeVisible()
    }
    await signIn(initialPassword); await account.goto('/security')
    await account.getByLabel('Текущий пароль').fill(initialPassword); await account.getByLabel('Новый пароль', { exact: true }).fill(changedPassword); await account.getByLabel('Повтор нового пароля').fill(changedPassword); await account.getByRole('button', { name: 'Изменить пароль', exact: true }).click()
    await expect(account.getByRole('heading', { name: 'Рады видеть вас' })).toBeVisible(); await signIn(changedPassword)
    await account.goto('/security'); await account.getByLabel('Текущий пароль').fill(changedPassword); await account.getByRole('button', { name: 'Настроить приложение', exact: true }).click()
    const secret = await account.getByLabel('Ключ приложения', { exact: true }).inputValue()
    await account.getByLabel('Код подтверждения', { exact: true }).fill(totp(secret)); await account.getByRole('button', { name: 'Включить защиту', exact: true }).click()
    await expect(account.getByRole('list', { name: 'Резервные коды' })).toBeVisible()
    const codes = await account.getByRole('list', { name: 'Резервные коды' }).getByRole('listitem').allTextContents(); expect(codes.length).toBe(8)
    expect(await account.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await account.getByRole('button', { name: 'Коды сохранены — перейти ко входу' }).click(); await signIn(changedPassword, codes[0])
    await account.goto('/forgot-password'); const beforeRecovery = new Set(readdirSync(mailbox))
    await account.getByLabel('Электронная почта').fill(email); await account.getByRole('button', { name: 'Отправить ссылку', exact: true }).click(); await expect(account.getByRole('status')).toContainText('Если аккаунт доступен')
    await expect.poll(() => Boolean(readLink(email, beforeRecovery))).toBe(true); await account.goto(readLink(email, beforeRecovery))
    await account.getByLabel('Новый пароль', { exact: true }).fill(recoveredPassword); await account.getByLabel('Повтор нового пароля').fill(recoveredPassword); await account.getByRole('button', { name: 'Сохранить новый пароль', exact: true }).click()
    await account.getByLabel('Код приложения или резервный код').fill(codes[1]!); await account.getByRole('button', { name: 'Сохранить новый пароль', exact: true }).click(); await expect(account.getByRole('status')).toContainText('Пароль изменён')
    await account.getByRole('link', { name: 'Вернуться ко входу' }).click(); await signIn(recoveredPassword, codes[2]); await account.goto('/security')
    await expect(account.getByText('Включена.', { exact: false })).toBeVisible()
  } finally {
    if (userID) { const response = await page.request.patch('/api/v1/team/' + userID, { headers: { Origin: new URL(page.url()).origin, 'X-CSRF-Token': me.csrf_token }, data: { role: 'recruiter', status: 'disabled' } }); expect(response.status()).toBe(200) }
    await staff.close()
  }
})
