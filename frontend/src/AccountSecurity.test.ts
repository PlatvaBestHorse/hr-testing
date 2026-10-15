import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import AccountSecurity from './AccountSecurity.vue'
import PasswordRecovery from './PasswordRecovery.vue'
import ResultComments from './ResultComments.vue'
import { api, ApiError, session } from './session'
vi.mock('./session', async original => ({ ...await original<typeof import('./session')>(), api: vi.fn() }))
const mocked = vi.mocked(api), wrappers: ReturnType<typeof mount>[] = []
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); mocked.mockReset(); session.user = null; vi.restoreAllMocks() })
async function screen(component: typeof AccountSecurity | typeof PasswordRecovery, path: string) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/security', component: AccountSecurity }, { path: '/reset-password', component: PasswordRecovery }, { path: '/login', component: { template: '<p>Вход</p>' } }] })
  await router.push(path); await router.isReady(); const wrapper = mount(component, { global: { plugins: [router] } }); wrappers.push(wrapper); await flushPromises(); return { wrapper, router }
}
describe('account security screens', () => {
  it('requires matching new passwords before making a mutation', async () => {
    mocked.mockResolvedValue({ mfa_enabled: false, mfa_available: true, recovery_codes_remaining: 0 })
    const { wrapper } = await screen(AccountSecurity, '/security')
    const inputs = wrapper.findAll('input[type=password]'); await inputs[0]!.setValue('fixture-current-password'); await inputs[1]!.setValue('fixture-new-password'); await inputs[2]!.setValue('different-password')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('одинаковый новый пароль'); expect(mocked).toHaveBeenCalledTimes(1)
  })
  it('shows confirmed backup codes before leaving the expired session', async () => {
    const codes = Array.from({ length: 8 }, (_, n) => 'FIXTURE-CODE-' + n)
    mocked.mockImplementation(async path => path === '/security' ? { mfa_enabled: false, mfa_available: true, recovery_codes_remaining: 0 } : path.endsWith('/setup') ? { secret: 'FIXTURE-SETUP-KEY' } : { recovery_codes: codes, sign_in_required: true })
    const { wrapper, router } = await screen(AccountSecurity, '/security')
    await wrapper.get('input[type=password]').setValue('fixture-current-password')
    await wrapper.findAll('button').find(b => b.text() === 'Настроить приложение')!.trigger('click'); await flushPromises()
    await wrapper.get('input[autocomplete=one-time-code]').setValue('123456')
    await wrapper.findAll('form')[1]!.trigger('submit'); await flushPromises()
    expect(wrapper.get('[aria-label="Резервные коды"]').findAll('li')).toHaveLength(8); expect(router.currentRoute.value.path).toBe('/security')
    await wrapper.findAll('button').find(b => b.text().startsWith('Коды сохранены'))!.trigger('click'); await flushPromises()
    expect(router.currentRoute.value.path).toBe('/login')
  })
  it('removes the reset token from the URL and retains it while requesting MFA', async () => {
    mocked.mockImplementation(async path => { if (path === '/auth/options') return { recovery_available: true, local_mail: true }; throw new ApiError(401, 'mfa_required', 'Введите второй фактор') })
    const { wrapper, router } = await screen(PasswordRecovery, '/reset-password#token=fixture-token')
    expect(router.currentRoute.value.hash).toBe('')
    const inputs = wrapper.findAll('input[type=password]'); await inputs[0]!.setValue('fixture-new-password'); await inputs[1]!.setValue('fixture-new-password')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.find('input[autocomplete=one-time-code]').exists()).toBe(true); expect(router.currentRoute.value.path).toBe('/reset-password')
    const call = mocked.mock.calls.find(([path]) => path.endsWith('/complete'))!
    expect(JSON.parse(String(call[1]!.body)).token).toBe('fixture-token')
  })
})
it('renders comment text safely and respects the server edit permissions', async () => {
  mocked.mockResolvedValue({ items: [{ id: 'comment', author_name: 'Fixture', body: '<img src=x onerror=alert(1)>', created_at: '2026-10-15T00:00:00Z', updated_at: '2026-10-15T00:00:00Z', can_edit: false, can_delete: false }], total: 1 })
  const wrapper = mount(ResultComments, { props: { attemptId: 'fixture' } }); wrappers.push(wrapper); await flushPromises()
  expect(wrapper.get('.comment-body').text()).toBe('<img src=x onerror=alert(1)>'); expect(wrapper.find('img').exists()).toBe(false)
  expect(wrapper.findAll('button').some(b => b.text().includes('Редактировать комментарий') || b.text().includes('Удалить комментарий'))).toBe(false)
})
