import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import Directory from './Directory.vue'
import { api, session } from './session'
vi.mock('./session', async importOriginal => ({ ...await importOriginal<typeof import('./session')>(), api: vi.fn() }))
const mocked = vi.mocked(api)
const wrappers: ReturnType<typeof mount>[] = []
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); mocked.mockReset(); session.user = null; session.loaded = false })
async function screen(erased: boolean) {
  session.user = { user_id: 'admin', organization_id: 'org', organization_name: 'Fixture', display_name: 'Fixture', email: 'fixture@example.test', role: 'org_admin', csrf_token: 'fixture' }
  mocked.mockResolvedValue({ id: 'candidate', full_name: erased ? 'Контактные данные удалены' : 'Fixture candidate', email: '', phone: '', status: erased ? 'archived' : 'active', erased_at: erased ? '2026-10-14T12:00:00Z' : null, retention_hold: false })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/candidates/:id', component: Directory }] })
  await router.push('/candidates/candidate'); await router.isReady()
  const wrapper = mount(Directory, { props: { kind: 'candidates' }, global: { plugins: [router] } }); wrappers.push(wrapper); await flushPromises(); return wrapper
}
describe('candidate contacts after retention', () => {
  it('locks erased contacts and cannot submit or issue invitations', async () => {
    const wrapper = await screen(true)
    expect(wrapper.text()).toContain('Редактирование и новые приглашения закрыты')
    for (const input of wrapper.findAll('input, select')) expect((input.element as HTMLInputElement).disabled).toBe(true)
    expect(wrapper.findAll('button').some(button => button.text() === 'Сохранить кандидата')).toBe(false)
    expect(wrapper.findAll('a').some(link => link.text() === 'Пригласить на тест')).toBe(false)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(mocked).toHaveBeenCalledTimes(1)
  })
  it('keeps a regular candidate editable for the administrator', async () => {
    const wrapper = await screen(false)
    expect((wrapper.get('input').element as HTMLInputElement).disabled).toBe(false)
    expect(wrapper.findAll('button').some(button => button.text() === 'Сохранить кандидата')).toBe(true)
  })
})
