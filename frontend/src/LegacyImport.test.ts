import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import LegacyImport from './LegacyImport.vue'
import { api } from './session'
vi.mock('./session', () => ({ api: vi.fn() }))
const mocked = vi.mocked(api), wrappers: ReturnType<typeof mount>[] = []
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); mocked.mockReset() })
function screen() { const w = mount(LegacyImport, { global: { stubs: { RouterLink: { template: '<a><slot /></a>' } } } }); wrappers.push(w); return w }
async function file(w: ReturnType<typeof mount>, text: string) {
  const input = w.get('input[type=file]')
  Object.defineProperty(input.element, 'files', { configurable: true, value: [{ name: 'bank.json', size: text.length, text: async () => text }] })
  await input.trigger('change'); await flushPromises()
}
const report = { valid: true, source_sha256: 'a'.repeat(64), question_count: 2, test_count: 1, categories: ['Логика'], tests: [{ source_id: 'test', title: 'Скрытый результат', question_count: 2, minutes: 20, passing: 70, shuffle: true, published: true, show_score: false }], errors: [], warnings: [], already_applied: false }
it('retains the original JSON for server duplicate-key checks and clears stale confirmation when the file changes', async () => {
  mocked.mockResolvedValue(report)
  const w = screen(), raw = '{"version":1,"version":2}'
  await file(w, raw); await w.get('button').trigger('click'); await flushPromises()
  expect(String(mocked.mock.calls[0]![1]!.body)).toContain(raw)
  expect(w.text()).toContain('Баллы скрыты от кандидата')
  await w.get('input[autocomplete=off]').setValue('ИМПОРТИРОВАТЬ')
  await file(w, '{"version":1}')
  expect(w.find('form').exists()).toBe(false); expect(w.text()).not.toContain('Скрытый результат')
  expect(mocked).toHaveBeenCalledTimes(1)
})
it('prevents applying an invalid preview or a previously imported file', async () => {
  mocked.mockResolvedValue({ ...report, valid: false, errors: [{ path: '/questions/0/correct', code: 'invalid_answer', message: 'Не найден правильный ответ' }] })
  const w = screen(); await file(w, '{"version":1}'); await w.get('button').trigger('click'); await flushPromises()
  expect(w.text()).toContain('/questions/0/correct'); expect(w.find('form').exists()).toBe(false)
  mocked.mockResolvedValue({ ...report, already_applied: true }); await w.get('button').trigger('click'); await flushPromises()
  expect(w.text()).toContain('уже импортирован'); expect(w.find('form').exists()).toBe(false)
})
