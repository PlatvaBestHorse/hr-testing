import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ConnectionStatus from './ConnectionStatus.vue'

afterEach(() => { vi.unstubAllGlobals() })
describe('connection status', () => {
  it('confirms readiness only after the server confirms migrations', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ status: 'ready' }) }))
    const wrapper = mount(ConnectionStatus)
    expect(wrapper.text()).toContain('Проверяем')
    await flushPromises()
    expect(wrapper.text()).toContain('Сервер и база данных доступны')
    wrapper.unmount()
  })
  it('reports failed requests and can retry', async () => {
    const fetch = vi.fn().mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ ok: true, json: async () => ({ status: 'ready' }) })
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(ConnectionStatus)
    await flushPromises()
    expect(wrapper.text()).toContain('Не удалось')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Сервер и база данных доступны')
    wrapper.unmount()
  })
  it('does not claim success for a service-unavailable response', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, json: async () => ({ status: 'unavailable' }) }))
    const wrapper = mount(ConnectionStatus)
    await flushPromises()
    expect(wrapper.text()).toContain('Не удалось')
    wrapper.unmount()
  })
})
