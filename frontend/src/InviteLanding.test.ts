import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import InviteLanding from './InviteLanding.vue'
import { api, ApiError } from './session'
vi.mock('./session', async importOriginal => ({ ...await importOriginal<typeof import('./session')>(), api: vi.fn() }))
const mocked = vi.mocked(api)
const snapshot = () => ({
  id: 'attempt', status: 'in_progress', title: 'Test', deadline_at: '2026-10-14T12:10:00Z', server_now: '2026-10-14T12:00:00Z', attempts_remaining: 0,
  items: [{ id: 'item', position: 1, type: 'multiple_choice', body: 'Choose', passage: '', revision: 0, selected_option_ids: [] as string[], options: [{ id: 'one', label: 'One' }, { id: 'two', label: 'Two' }] }],
})
const wrappers: ReturnType<typeof mount>[] = []
async function screen() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/i/:token', component: InviteLanding }] })
  await router.push('/i/fixture'); await router.isReady()
  const wrapper = mount(InviteLanding, { global: { plugins: [router] } }); wrappers.push(wrapper)
  await flushPromises(); return wrapper
}
beforeEach(() => { mocked.mockReset() })
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })
describe('candidate answer persistence', () => {
  it('shows the consent version and sends the version the candidate accepted', async () => {
    mocked.mockImplementation(async (path) => {
      if (path.endsWith('/attempt')) throw new ApiError(404, 'not_found', 'No attempt')
      if (path.endsWith('/start')) return snapshot()
      return { title: 'Test', description: '', organization_name: 'Fixture', duration_seconds: 600, question_count: 1, attempts_remaining: 1, max_attempts: 1, expires_at: '2026-10-16T12:00:00Z', consent_document: { id: 'document', revision: 2, approved: true, operator_name: 'Fixture operator', contact: 'privacy@example.test', consent_text: 'Consent version two', policy_text: 'Policy version two' } }
    })
    const wrapper = await screen()
    expect(wrapper.text()).toContain('Согласие и политика обработки данных · версия 2')
    expect(wrapper.text()).toContain('Consent version two')
    await wrapper.get('input[type=checkbox]').setValue(true)
    await wrapper.findAll('button').find(button => button.text() === 'Начать тест')!.trigger('click'); await flushPromises()
    const request = mocked.mock.calls.find(([path]) => path.endsWith('/start'))
    expect(JSON.parse(String(request?.[1]?.body)).consent_revision).toBe(2)
  })
  it('restores confirmed answers without starting another attempt', async () => {
    const data = snapshot(); data.items[0]!.selected_option_ids = ['two']; data.items[0]!.revision = 4
    mocked.mockResolvedValue(data)
    const wrapper = await screen()
    expect((wrapper.findAll('input')[1]!.element as HTMLInputElement).checked).toBe(true)
    expect(mocked).toHaveBeenCalledTimes(1)
    expect(mocked.mock.calls[0]![0]).toBe('/invite/fixture/attempt')
  })
  it('waits for acknowledgement and serializes rapid edits with confirmed revisions', async () => {
    let acknowledge!: (value: unknown) => void
    mocked.mockResolvedValueOnce(snapshot()).mockImplementationOnce(() => new Promise(resolve => { acknowledge = resolve })).mockResolvedValueOnce({ revision: 2, selected_option_ids: ['one', 'two'] })
    const wrapper = await screen()
    await wrapper.findAll('input')[0]!.setValue(true)
    await wrapper.findAll('input')[1]!.setValue(true)
    expect(wrapper.get('[role="status"]').text()).toBe('Сохраняем ответы…')
    expect(mocked).toHaveBeenCalledTimes(2)
    acknowledge({ revision: 1, selected_option_ids: ['one'] }); await flushPromises()
    expect(mocked).toHaveBeenCalledTimes(3)
    const second = JSON.parse(String(mocked.mock.calls[2]![1]?.body))
    expect(second.expected_revision).toBe(1); expect(second.selected_option_ids).toEqual(['one', 'two'])
    expect(wrapper.get('[role="status"]').text()).toBe('Все изменения сохранены')
  })
  it('retries a lost response with the same request id and never claims offline saving', async () => {
    mocked.mockResolvedValueOnce(snapshot()).mockRejectedValueOnce(new ApiError(0, 'offline', 'Нет соединения'))
      .mockResolvedValueOnce({ revision: 1, selected_option_ids: ['one'] }).mockResolvedValueOnce(snapshot())
    const wrapper = await screen()
    await wrapper.findAll('input')[0]!.setValue(true); await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('Нет подтверждения сохранения')
    const original = JSON.parse(String(mocked.mock.calls[1]![1]?.body))
    await wrapper.findAll('button').find(button => button.text() === 'Повторить сохранение')!.trigger('click'); await flushPromises()
    const retry = JSON.parse(String(mocked.mock.calls[2]![1]?.body))
    expect(retry).toEqual(original)
    expect(wrapper.get('[role="status"]').text()).toBe('Все изменения сохранены')
  })
  it('blocks a stale tab instead of overwriting a newer server answer', async () => {
    mocked.mockResolvedValueOnce(snapshot()).mockRejectedValueOnce(new ApiError(409, 'revision_conflict', 'Изменено в другой вкладке'))
    const wrapper = await screen()
    await wrapper.findAll('input')[0]!.setValue(true); await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('другой вкладке')
    expect((wrapper.get('fieldset').element as HTMLFieldSetElement).disabled).toBe(true)
    const updated = snapshot(); updated.items[0]!.selected_option_ids = ['two']; updated.items[0]!.revision = 2
    mocked.mockResolvedValueOnce(updated); vi.stubGlobal('confirm', vi.fn(() => true))
    await wrapper.findAll('button').find(button => button.text() === 'Обновить сохранённые ответы')!.trigger('click'); await flushPromises()
    expect((wrapper.findAll('input')[1]!.element as HTMLInputElement).checked).toBe(true)
    expect((wrapper.findAll('input')[0]!.element as HTMLInputElement).checked).toBe(false)
  })
  it('requests expiry completion when the server deadline arrives', async () => {
    vi.useFakeTimers()
    const data = snapshot(); data.deadline_at = '2026-10-14T12:00:01Z'
    mocked.mockResolvedValueOnce(data).mockResolvedValueOnce({ ...data, status: 'expired', items: [] })
    const wrapper = await screen()
    await vi.advanceTimersByTimeAsync(1100); await flushPromises()
    expect(mocked.mock.calls[1]![0]).toBe('/invite/fixture/attempts/attempt/submit')
    expect(wrapper.text()).toContain('Время вышло')
  })
})
