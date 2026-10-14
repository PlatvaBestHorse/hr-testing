import { ApiError, forgetSession } from './session'
export async function downloadResult(path: string, filename: string) {
  let response: Response
  try { response = await fetch('/api/v1' + path, { credentials: 'same-origin', cache: 'no-store', signal: AbortSignal.timeout(30000) }) }
  catch { throw new Error('Нет соединения с сервером. Файл не получен') }
  if (!response.ok) {
    if (response.status === 401) forgetSession()
    const body = await response.json().catch(() => ({}))
    throw new ApiError(response.status, body.code || 'export_failed', body.message || 'Не удалось выгрузить результаты')
  }
  const blob = await response.blob(), url = URL.createObjectURL(blob)
  const link = document.createElement('a'); link.href = url; link.download = filename
  document.body.append(link); link.click(); link.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
