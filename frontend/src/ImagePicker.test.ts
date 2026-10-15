import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ImagePicker from './ImagePicker.vue'
import QuestionImage from './QuestionImage.vue'
import { session } from './session'
const fetchMock = vi.fn()
const wrappers: ReturnType<typeof mount>[] = []
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); vi.unstubAllGlobals(); fetchMock.mockReset(); session.user = null })
async function select(wrapper: ReturnType<typeof mount>, file: File) {
  const input = wrapper.get('input[type=file]'); Object.defineProperty(input.element, 'files', { value: [file], configurable: true }); await input.trigger('change'); await flushPromises()
}
describe('image upload', () => {
  it('sends multipart with CSRF and uses the confirmed server ID', async () => {
    vi.stubGlobal('fetch', fetchMock)
    session.user = { user_id: 'fixture', organization_id: 'fixture', organization_name: 'Fixture', display_name: 'Fixture', email: 'fixture@example.test', role: 'recruiter', csrf_token: 'fixture-csrf' }
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ id: 'confirmed-image' }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    const wrapper = mount(ImagePicker, { props: { modelValue: '', label: 'Изображение вопроса' } }); wrappers.push(wrapper)
    await select(wrapper, new File(['fixture'], 'fixture.png', { type: 'image/png' }))
    const [path, options] = fetchMock.mock.calls[0]!
    expect(path).toBe('/api/v1/images'); expect(options.body).toBeInstanceOf(FormData)
    expect(options.headers.get('Content-Type')).toBeNull(); expect(options.headers.get('X-CSRF-Token')).toBe('fixture-csrf')
    expect(wrapper.emitted('update:modelValue')).toEqual([['confirmed-image']]); expect(wrapper.emitted('busy')).toEqual([[true], [false]])
  })
  it('keeps the previous image when the server rejects the replacement', async () => {
    vi.stubGlobal('fetch', fetchMock)
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ code: 'invalid_image', message: 'Повреждённый файл' }), { status: 422 }))
    const wrapper = mount(ImagePicker, { props: { modelValue: 'previous', label: 'Изображение вопроса' } }); wrappers.push(wrapper)
    await select(wrapper, new File(['bad'], 'bad.png', { type: 'image/png' }))
    expect(wrapper.text()).toContain('Повреждённый файл'); expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('img').attributes('src')).toBe('/api/v1/images/previous')
  })
  it('rejects unsupported and oversized files without a request', async () => {
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(ImagePicker, { props: { modelValue: '', label: 'Изображение вопроса' } }); wrappers.push(wrapper)
    await select(wrapper, new File(['svg'], 'image.svg', { type: 'image/svg+xml' })); expect(wrapper.text()).toContain('Выберите PNG или JPEG')
    await select(wrapper, new File([new Uint8Array(5 * 1024 * 1024 + 1)], 'large.png', { type: 'image/png' })); expect(wrapper.text()).toContain('Максимальный размер')
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it('removes only the image reference and obeys a disabled editor', async () => {
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(ImagePicker, { props: { modelValue: 'old-version-image', label: 'Изображение вопроса', disabled: true } }); wrappers.push(wrapper)
    await select(wrapper, new File(['fixture'], 'fixture.png', { type: 'image/png' })); expect(fetchMock).not.toHaveBeenCalled()
    expect((wrapper.get('button').element as HTMLButtonElement).disabled).toBe(true)
    await wrapper.setProps({ disabled: false }); await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['']]); expect(fetchMock).not.toHaveBeenCalled()
  })
  it('aborts an upload when the editor changes and cannot attach a late response', async () => {
    vi.stubGlobal('fetch', fetchMock)
    let resolve!: (response: Response) => void
    fetchMock.mockImplementation(() => new Promise<Response>(value => { resolve = value }))
    const busyChanged = vi.fn(), imageChanged = vi.fn()
    const wrapper = mount(ImagePicker, { props: { modelValue: '', label: 'Изображение вопроса', onBusy: busyChanged, 'onUpdate:modelValue': imageChanged } })
    await select(wrapper, new File(['fixture'], 'fixture.png', { type: 'image/png' }))
    const signal = fetchMock.mock.calls[0]![1].signal as AbortSignal
    wrapper.unmount(); expect(signal.aborted).toBe(true)
    resolve(new Response(JSON.stringify({ id: 'late-response' }), { status: 201 })); await flushPromises()
    expect(imageChanged).not.toHaveBeenCalled(); expect(busyChanged.mock.calls).toEqual([[true], [false]])
  })
})
it('retries image loading and resets errors when the question changes', async () => {
  const wrapper = mount(QuestionImage, { props: { src: '/api/v1/images/one', alt: 'Изображение вопроса' } }); wrappers.push(wrapper)
  await wrapper.get('img').trigger('error'); expect(wrapper.text()).toContain('Изображение не загрузилось')
  await wrapper.get('button').trigger('click'); expect(wrapper.get('img').attributes('src')).toBe('/api/v1/images/one?retry=1')
  await wrapper.get('img').trigger('error'); await wrapper.setProps({ src: '/api/v1/images/two' }); expect(wrapper.get('img').attributes('src')).toBe('/api/v1/images/two')
})
