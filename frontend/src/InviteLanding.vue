<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Layers, Clock, ClipboardList } from '@lucide/vue'
import { api, ApiError } from './session'
import { formatDate, formatDuration } from './domain'
interface Info { title: string; description: string; organization_name: string; duration_seconds: number; question_count: number; attempts_remaining: number; max_attempts: number; expires_at: string }
interface Item { id: string; position: number; type: string; body: string; passage: string; options: { id: string; label: string }[]; selected_option_ids: string[]; revision: number }
interface Attempt { id: string; status: string; title: string; deadline_at: string; server_now: string; attempts_remaining: number; items: Item[]; score?: { percent: string; passed: boolean } }
interface Receipt { revision: number; selected_option_ids: string[] }
interface Pending { request_id: string; expected_revision: number; selected_option_ids: string[] }
interface LocalAnswer { selected: string[]; confirmed: string[]; revision: number; pending?: Pending; saving: boolean; error: string; blocked: boolean }
const route = useRoute(), prefix = '/invite/' + encodeURIComponent(String(route.params.token))
const info = ref<Info | null>(null), attempt = ref<Attempt | null>(null), answers = ref<Record<string, LocalAnswer>>({})
const error = ref(''), notice = ref(''), loading = ref(true), busy = ref(false), consent = ref(false), index = ref(0), showConfirm = ref(false), remaining = ref(0)
let budget = 0, synchronizedAt = 0, alive = true, finishing = false, refreshing = false
let ticker: ReturnType<typeof setInterval>, poller: ReturnType<typeof setInterval>
const equal = (a: string[], b: string[]) => JSON.stringify([...a].sort()) === JSON.stringify([...b].sort())
const active = computed(() => attempt.value?.status === 'in_progress')
const current = computed(() => attempt.value?.items[index.value])
const dirty = computed(() => Object.values(answers.value).some(a => a.pending || a.saving || !equal(a.selected, a.confirmed)))
const answeredCount = computed(() => Object.values(answers.value).filter(a => a.selected.length > 0).length)
const timerText = computed(() => { const n = Math.max(0, Math.ceil(remaining.value)); return Math.floor(n / 60).toString().padStart(2, '0') + ':' + (n % 60).toString().padStart(2, '0') })
const statusText = computed(() => {
  if (Object.values(answers.value).some(a => a.blocked)) return 'Ответ изменён в другой вкладке. Обновите сохранённые ответы.'
  if (Object.values(answers.value).some(a => a.error)) return 'Нет подтверждения сохранения. Не закрывайте страницу.'
  return dirty.value ? 'Сохраняем ответы…' : 'Все изменения сохранены'
})
async function request<T>(path: string, options: RequestInit = {}) { return api<T>(prefix + path, { ...options, signal: AbortSignal.timeout(12000) }) }
function applySnapshot(data: Attempt, reset = false) {
  if (!alive) return
  attempt.value = data
  budget = Math.max(0, (Date.parse(data.deadline_at) - Date.parse(data.server_now)) / 1000)
  synchronizedAt = performance.now(); remaining.value = budget
  if (reset) answers.value = {}
  for (const item of data.items) {
    const local = answers.value[item.id]
    if (!local || (!local.pending && !local.saving && equal(local.selected, local.confirmed) && item.revision >= local.revision)) {
      answers.value[item.id] = { selected: [...item.selected_option_ids], confirmed: [...item.selected_option_ids], revision: item.revision, saving: false, error: '', blocked: false }
    }
  }
  if (data.status !== 'in_progress') {
    showConfirm.value = false
    if (dirty.value) notice.value = 'Попытка завершена. Изменения без подтверждения сервера в результат не включены.'
  }
}
async function load() {
  error.value = ''; loading.value = true
  try {
    try { applySnapshot(await request<Attempt>('/attempt'), true); return }
    catch (err) { if (!(err instanceof ApiError) || err.status !== 404) throw err }
    info.value = await request<Info>('')
  } catch (err) { error.value = (err as Error).message }
  finally { loading.value = false }
}
let startRequest = crypto.randomUUID()
async function start(newAttempt = false) {
  if (busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    applySnapshot(await request<Attempt>('/start', { method: 'POST', body: JSON.stringify({ request_id: startRequest, consent: consent.value, new_attempt: newAttempt }) }), true)
    startRequest = crypto.randomUUID(); index.value = 0
  } catch (err) { error.value = (err as Error).message }
  finally { busy.value = false }
}
function choose(item: Item, option: string) {
  if (!active.value || remaining.value <= 0 || busy.value) return
  const local = answers.value[item.id]!
  if (local.blocked) return
  local.selected = item.type === 'multiple_choice' ? local.selected.includes(option) ? local.selected.filter(id => id !== option) : [...local.selected, option] : [option]
  void save(item.id)
}
async function save(itemID: string): Promise<boolean> {
  const local = answers.value[itemID]!
  if (local.saving || local.blocked || !active.value || remaining.value <= 0) return false
  local.saving = true
  try {
    while (alive && active.value && (local.pending || !equal(local.selected, local.confirmed))) {
      local.pending ||= { request_id: crypto.randomUUID(), expected_revision: local.revision, selected_option_ids: [...local.selected] }
      const receipt = await request<Receipt>('/attempts/' + attempt.value!.id + '/items/' + itemID + '/answer', { method: 'PUT', body: JSON.stringify(local.pending) })
      local.revision = receipt.revision; local.confirmed = [...receipt.selected_option_ids]; local.pending = undefined; local.error = ''
    }
    return true
  } catch (err) {
    local.error = (err as Error).message
    if (err instanceof ApiError && err.code === 'revision_conflict') { local.blocked = true; local.pending = undefined }
    if (err instanceof ApiError && (err.code === 'attempt_finished' || err.status === 404)) await refresh()
    return false
  } finally { local.saving = false }
}
async function refresh() {
  if (refreshing || !attempt.value) return
  refreshing = true
  try { applySnapshot(await request<Attempt>('/attempt')); if (active.value) error.value = '' }
  catch (err) { error.value = (err as Error).message; if (err instanceof ApiError && err.status === 404) { attempt.value = null; info.value = null } }
  finally { refreshing = false }
}
async function restoreConfirmed() {
  if (!window.confirm('Загрузить ответы с сервера? Неподтверждённые изменения в этой вкладке будут удалены.')) return
  busy.value = true
  try { applySnapshot(await request<Attempt>('/attempt'), true); error.value = '' }
  catch (err) { error.value = (err as Error).message }
  finally { busy.value = false }
}
async function flush(): Promise<boolean> {
  while (Object.values(answers.value).some(a => a.saving)) await new Promise(resolve => setTimeout(resolve, 30))
  for (const item of attempt.value!.items) {
    const local = answers.value[item.id]!
    if ((local.pending || !equal(local.selected, local.confirmed)) && !await save(item.id)) return false
  }
  return !dirty.value
}
async function submit(expired = false) {
  if (finishing || !attempt.value || (!expired && busy.value)) return
  finishing = true; busy.value = true; error.value = ''
  try {
    if (!expired && remaining.value > 0 && !await flush()) { error.value = 'Сначала дождитесь сохранения ответов или повторите отправку.'; return }
    applySnapshot(await request<Attempt>('/attempts/' + attempt.value.id + '/submit', { method: 'POST', body: '{}' }))
  } catch (err) { error.value = (err as Error).message }
  finally { busy.value = false; finishing = false }
}
function beforeUnload(event: BeforeUnloadEvent) { if (active.value && dirty.value) { event.preventDefault(); event.returnValue = '' } }
function reconnect() { for (const id of Object.keys(answers.value)) void save(id); void refresh() }
onMounted(() => {
  void load()
  ticker = setInterval(() => { remaining.value = Math.max(0, budget - (performance.now() - synchronizedAt) / 1000); if (active.value && remaining.value <= 0) void submit(true) }, 1000)
  poller = setInterval(() => {
    if (!active.value || finishing) return
    void refresh()
    for (const [id, local] of Object.entries(answers.value)) if (!local.blocked && !local.saving && (local.pending || !equal(local.selected, local.confirmed))) void save(id)
  }, 5000)
  window.addEventListener('online', reconnect); window.addEventListener('beforeunload', beforeUnload)
})
onBeforeUnmount(() => { alive = false; clearInterval(ticker); clearInterval(poller); window.removeEventListener('online', reconnect); window.removeEventListener('beforeunload', beforeUnload) })
</script>
<template>
  <main class="candidate-page passage-page">
    <div class="login-brand"><span class="brand-icon"><Layers :size="25" /></span><strong>Кадровый тестер</strong></div>
    <section class="candidate-card">
      <p v-if="loading" class="loading-state" role="status">Проверяем приглашение…</p>
      <template v-else-if="attempt">
        <p class="eyebrow">{{ active ? 'ПРОХОЖДЕНИЕ ТЕСТА' : 'ПОПЫТКА ЗАВЕРШЕНА' }}</p><h1>{{ attempt.title }}</h1>
        <template v-if="active && current">
          <div class="passage-meta"><strong class="passage-timer" :class="{ urgent: remaining < 60 }" aria-label="Оставшееся время"><Clock :size="18" />{{ timerText }}</strong><span>Вопрос {{ index + 1 }} из {{ attempt.items.length }}</span></div>
          <p class="save-status" role="status">{{ statusText }}</p>
          <p v-if="remaining <= 0" class="form-error" role="alert">Время вышло. Сервер завершает попытку по сохранённым ответам.</p>
          <div v-if="current.passage" class="question-passage">{{ current.passage }}</div><h2 class="passage-question">{{ current.body }}</h2>
          <p class="muted-text">{{ current.type === 'multiple_choice' ? 'Выберите все подходящие варианты.' : 'Выберите один вариант.' }}</p>
          <fieldset class="passage-options" :disabled="busy || remaining <= 0 || answers[current.id]?.blocked">
            <legend class="sr-only">Варианты ответа</legend>
            <label v-for="option in current.options" :key="option.id" :class="{ chosen: answers[current.id]?.selected.includes(option.id) }">
              <input :type="current.type === 'multiple_choice' ? 'checkbox' : 'radio'" :name="current.id" :checked="answers[current.id]?.selected.includes(option.id)" @change="choose(current, option.id)" /><span>{{ option.label }}</span>
            </label>
          </fieldset>
          <p v-if="answers[current.id]?.error" class="form-error" role="alert">{{ answers[current.id]?.error }}</p>
          <div class="passage-actions"><button class="secondary-button" :disabled="index === 0 || busy" @click="index--">Назад</button><button v-if="index < attempt.items.length - 1" class="primary-button" :disabled="busy" @click="index++">Далее</button><button v-else class="primary-button" :disabled="busy || remaining <= 0" @click="showConfirm = true">Завершить тест</button></div>
          <nav class="question-navigation" aria-label="Вопросы теста"><button v-for="(item, n) in attempt.items" :key="item.id" :aria-label="'Вопрос ' + (n + 1)" :aria-current="n === index ? 'step' : undefined" :class="{ answered: answers[item.id]?.confirmed.length, current: n === index }" :disabled="busy" @click="index = n">{{ n + 1 }}</button></nav>
          <p class="muted-text">Сохранено ответов: {{ Object.values(answers).filter(a => a.confirmed.length).length }} из {{ attempt.items.length }}. После перезагрузки восстановятся подтверждённые ответы.</p>
          <button v-if="dirty" class="secondary-button" :disabled="busy" @click="reconnect">Повторить сохранение</button>
          <button v-if="Object.values(answers).some(a => a.blocked)" class="secondary-button" :disabled="busy" @click="restoreConfirmed">Обновить сохранённые ответы</button>
          <section v-if="showConfirm" class="submit-confirm" aria-label="Подтверждение завершения"><h2>Завершить попытку?</h2><p>Вопросов без ответа: {{ attempt.items.length - answeredCount }}. После завершения ответы изменить нельзя.</p><p v-if="dirty">Перед завершением дождёмся подтверждения сохранения.</p><button class="primary-button" :disabled="busy" @click="submit()">{{ busy ? 'Завершаем…' : 'Подтвердить завершение' }}</button><button class="secondary-button" :disabled="busy" @click="showConfirm = false">Продолжить тест</button></section>
        </template>
        <template v-else>
          <h2>{{ attempt.status === 'expired' ? 'Время вышло' : attempt.status === 'cancelled' ? 'Попытка отменена' : 'Тест завершён' }}</h2><p>Ответы обработаны. Результат доступен HR.</p>
          <p v-if="notice" class="form-error">{{ notice }}</p>
          <div v-if="attempt.score" class="candidate-score"><strong>{{ attempt.score.percent }}%</strong><p>{{ attempt.score.passed ? 'Проходной балл достигнут' : 'Проходной балл не достигнут' }}</p></div><p v-else class="muted-text">Показ баллов кандидату выключен в настройках теста.</p>
          <template v-if="attempt.attempts_remaining > 0 && attempt.status !== 'cancelled'"><label class="passage-consent"><input v-model="consent" type="checkbox" />Я ознакомился с правилами и согласен на обработку ответов для оценки.</label><button class="primary-button" :disabled="busy || !consent" @click="start(true)">Начать новую попытку</button></template>
        </template>
      </template>
      <template v-else-if="info">
        <p class="eyebrow">ПЕРСОНАЛЬНОЕ ПРИГЛАШЕНИЕ</p><p class="muted-text">{{ info.organization_name }}</p><h1>{{ info.title }}</h1><p v-if="info.description" class="candidate-description">{{ info.description }}</p>
        <div class="invite-facts"><div><Clock :size="20" /><strong>{{ formatDuration(info.duration_seconds) }}</strong><span>на прохождение</span></div><div><ClipboardList :size="20" /><strong>{{ info.question_count }}</strong><span>вопросов</span></div></div><p class="muted-text">Попыток осталось: {{ info.attempts_remaining }} из {{ info.max_attempts }}.</p><p class="muted-text">Ссылка действует до {{ formatDate(info.expires_at) }}.</p>
        <div class="candidate-stage-note">Таймер начнётся после нажатия «Начать тест». Ответы сохраняются автоматически. Перезагрузка страницы не останавливает таймер. Без соединения изменения не сохраняются; при истечении времени учитываются только ответы, принятые сервером. Если срок ссылки наступит раньше, тест завершится в этот срок.</div>
        <p class="muted-text">Ответы и время прохождения передаются организации {{ info.organization_name }} для оценки. Это среда разработки: используйте только тестовые данные.</p>
        <label class="passage-consent"><input v-model="consent" type="checkbox" />Я ознакомился с правилами и согласен на обработку ответов для оценки.</label><button class="primary-button" :disabled="busy || !consent" @click="start()">{{ busy ? 'Начинаем…' : 'Начать тест' }}</button>
      </template>
      <template v-else-if="error"><h1>Приглашение недоступно</h1><p>Свяжитесь с HR, чтобы получить действующую ссылку.</p><button class="secondary-button" @click="load">Проверить ещё раз</button></template>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </section><p class="candidate-footer">Персональная ссылка предназначена только для приглашённого кандидата.</p>
  </main>
</template>
