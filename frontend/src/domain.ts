export interface List<T> { items: T[]; total: number; page: number; page_size: number }
export interface TestItem { id?: string; question_id: string; question_version_id: string; question_version_no: number; body: string; type: string; position: number; points: string }
export interface TestRecord {
  id: string; status: string; version_id: string; version_no: number; latest_version_no: number; version_status: string
  title: string; description: string; duration_seconds: number; pass_percent: string
  shuffle_questions: boolean; shuffle_options: boolean; show_candidate_score: boolean
  question_count: number; max_points: string; items: TestItem[]; versions: { version_no: number; status: string }[]
}
export interface Candidate { id: string; full_name: string; email: string; phone: string; status: string; created_at: string }
export interface Vacancy { id: string; title: string; department: string; status: string; assessor_ids: string[] }
export interface Invitation {
  id: string; candidate_id: string; candidate_name: string; test_version_id: string; test_title: string; test_version_no: number
  vacancy_id: string; vacancy_title: string; status: string; max_attempts: number; used_attempts: number; expires_at: string; issued_at: string
}
export function formatDate(value: string) {
  return new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Europe/Moscow' }).format(new Date(value)) + ' МСК'
}
export function formatDuration(seconds: number) {
  const minutes = Math.floor(seconds / 60), remainder = seconds % 60
  return minutes + ' мин' + (remainder ? ' ' + remainder + ' с' : '')
}
export function dateInput(value: Date) {
  const parts = new Intl.DateTimeFormat('sv-SE', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: 'Europe/Moscow', hourCycle: 'h23' }).format(value)
  return parts.replace(' ', 'T')
}
