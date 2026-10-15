import { reactive } from 'vue'

export type Role = 'org_admin' | 'recruiter' | 'assessor'
export interface CurrentUser {
  user_id: string; organization_id: string; organization_name: string
  display_name: string; email: string; role: Role; csrf_token: string
}
export const session = reactive<{ user: CurrentUser | null; loaded: boolean }>({ user: null, loaded: false })
export const roleNames: Record<Role, string> = { org_admin: 'Администратор', recruiter: 'HR / рекрутер', assessor: 'Руководитель-оценщик' }

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public details?: string[]) { super(message) }
}
export function forgetSession(notify = true) {
  session.user = null; session.loaded = true
  if (notify) window.dispatchEvent(new Event('hr-session-expired'))
}
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body && !(options.body instanceof FormData)) headers.set('Content-Type', 'application/json')
  if (options.method && !['GET', 'HEAD'].includes(options.method) && session.user) headers.set('X-CSRF-Token', session.user.csrf_token)
  let response: Response
  try { response = await fetch('/api/v1' + path, { ...options, headers, credentials: 'same-origin', cache: 'no-store' }) }
  catch { throw new ApiError(0, 'offline', 'Нет соединения с сервером. Изменения не сохранены') }
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    if (response.status === 401 && path !== '/auth/login' && path !== '/me' && !path.startsWith('/auth/recovery/') && !['mfa_required', 'verification_failed'].includes(body.code)) forgetSession()
    const details = Array.isArray(body.errors) ? body.errors.filter((item: unknown) => typeof item === 'string') : undefined
    throw new ApiError(response.status, body.code || 'request_failed', (body.message || 'Не удалось выполнить запрос') + (details?.length ? ': ' + details.join('; ') : ''), details)
  }
  return response.status === 204 ? undefined as T : response.json()
}
let pending: Promise<void> | undefined
export async function loadSession(force = false) {
  if (session.loaded && !force) return
  if (pending) return pending
  pending = (async () => {
    try { session.user = await api<CurrentUser>('/me') }
    catch (error) { session.user = null; if (!(error instanceof ApiError) || error.status !== 401) throw error }
    finally { session.loaded = true; pending = undefined }
  })()
  return pending
}
export async function login(email: string, password: string, second_factor = '') {
  session.user = await api<CurrentUser>('/auth/login', { method: 'POST', body: JSON.stringify({ email, password, second_factor }) })
  session.loaded = true
}
export async function logout() {
  await api('/auth/logout', { method: 'POST' })
  forgetSession()
}
