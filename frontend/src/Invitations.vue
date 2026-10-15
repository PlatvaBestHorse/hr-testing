<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Link, Copy, ArrowUpRight, Plus } from '@lucide/vue'
import { api } from './session'
import { dateInput, formatDate, type Candidate, type Invitation, type List, type TestRecord, type Vacancy } from './domain'
const route = useRoute()
const items = ref<Invitation[]>([]), candidates = ref<Candidate[]>([]), tests = ref<TestRecord[]>([]), vacancies = ref<Vacancy[]>([]), page = ref(1), total = ref(0), status = ref(''), error = ref(''), notice = ref(''), busy = ref(false), loading = ref(false), newLink = ref(''), newInvitationID = ref('')
const searches = reactive({ candidate: '', test: '', vacancy: '' })
const form = reactive({ candidate_id: '', test_version_id: '', vacancy_id: '', max_attempts: 1, expires_at: dateInput(new Date(Date.now() + 24 * 60 * 60 * 1000)) })
const labels: Record<string, string> = { issued: 'Выдано', revoked: 'Отозвано', expired: 'Истекло', completed: 'Завершено' }
let generation = 0
async function load() {
  const current = ++generation; loading.value = true
  try { const data = await api<List<Invitation>>('/invitations?' + new URLSearchParams({ page: String(page.value), status: status.value, candidate_id: String(route.query.candidate_id || '') })); if (current === generation) { items.value = data.items; total.value = data.total } }
  catch (err) { if (current === generation) error.value = (err as Error).message } finally { if (current === generation) loading.value = false }
}
async function searchCandidates() { try { candidates.value = (await api<List<Candidate>>('/candidates?' + new URLSearchParams({ search: searches.candidate, status: 'active' }))).items } catch (err) { error.value = (err as Error).message } }
async function searchTests() { try { tests.value = (await api<List<TestRecord>>('/tests?' + new URLSearchParams({ search: searches.test, published_only: 'true' }))).items } catch (err) { error.value = (err as Error).message } }
async function searchVacancies() { try { vacancies.value = (await api<List<Vacancy>>('/vacancies?' + new URLSearchParams({ search: searches.vacancy, status: 'open' }))).items } catch (err) { error.value = (err as Error).message } }
async function issue() {
  if (newLink.value && !window.confirm('Вы сохранили текущую ссылку? После выдачи нового приглашения она исчезнет с этого экрана.')) return
  busy.value = true; error.value = ''; notice.value = ''
  try { const data = await api<{ invitation: Invitation; path: string }>('/invitations', { method: 'POST', body: JSON.stringify({ ...form, expires_at: form.expires_at + ':00+03:00' }) }); newLink.value = new URL(data.path, window.location.origin).href; newInvitationID.value = data.invitation.id; notice.value = 'Приглашение выдано. Сохраните ссылку — она показывается только сейчас.'; await load() }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
async function copy() { try { await navigator.clipboard.writeText(newLink.value); notice.value = 'Ссылка скопирована' } catch { notice.value = 'Выделите ссылку и скопируйте её вручную: браузер не разрешил копирование.' } }
async function revoke(i: Invitation) {
  if (!window.confirm('Отозвать приглашение для «' + i.candidate_name + '»? Ссылка перестанет работать.')) return
  busy.value = true; error.value = ''; try { await api('/invitations/' + i.id + '/revoke', { method: 'POST' }); if (i.id === newInvitationID.value) newLink.value = ''; notice.value = 'Приглашение отозвано'; await load() } catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
async function init() {
  try { const settings = await api<{ invitation_default_days: number }>('/organization'); form.expires_at = dateInput(new Date(Date.now() + settings.invitation_default_days * 24 * 60 * 60 * 1000)) } catch (err) { error.value = (err as Error).message }
  await Promise.all([load(), searchCandidates(), searchTests(), searchVacancies()])
  if (route.query.candidate_id) { try { const c = await api<Candidate>('/candidates/' + route.query.candidate_id); if (!candidates.value.some(v => v.id === c.id)) candidates.value.unshift(c); form.candidate_id = c.id } catch (err) { error.value = (err as Error).message } }
  if (route.query.test_id && route.query.test_version_no) { try { const t = await api<TestRecord>('/tests/' + route.query.test_id + '?version_no=' + route.query.test_version_no); if (t.version_status === 'published' && t.status === 'active') { if (!tests.value.some(v => v.version_id === t.version_id)) tests.value.unshift(t); form.test_version_id = t.version_id } } catch (err) { error.value = (err as Error).message } }
}
watch(status, () => { page.value = 1; load() }); onMounted(init)
</script>
<template>
  <div class="page-heading"><div><p class="eyebrow">ПЕРСОНАЛЬНЫЕ ССЫЛКИ</p><h1>Приглашения</h1><p class="subtitle">Опубликованный тест, срок действия и допустимое число попыток.</p></div><Link :size="27" /></div>
  <p v-if="error" class="form-error" role="alert">{{ error }}</p><p v-if="notice" class="form-success" role="status">{{ notice }}</p>
  <section v-if="newLink" class="issued-link"><h2>Ссылка для кандидата</h2><p>Показывается один раз. Не публикуйте её в открытом доступе.</p><label class="form-field">Ссылка приглашения<input :value="newLink" readonly @focus="($event.target as HTMLInputElement).select()" /></label><div class="form-actions"><button class="secondary-button" @click="copy"><Copy :size="16" />Копировать</button><a :href="newLink" target="_blank" rel="noopener noreferrer" class="secondary-button">Открыть приглашение <ArrowUpRight :size="16" /></a></div></section>
  <section class="editor-card"><h2>Выдать приглашение</h2><form class="invitation-form" @submit.prevent="issue">
    <div><label class="form-field">Поиск кандидата<input v-model="searches.candidate" placeholder="Имя или почта" maxlength="200" /></label><button type="button" class="text-button" @click="searchCandidates">Найти кандидата</button><label class="form-field">Кандидат<select v-model="form.candidate_id" aria-label="Кандидат приглашения" required><option value="" disabled>Выберите кандидата</option><option v-for="c in candidates" :key="c.id" :value="c.id">{{ c.full_name }}{{ c.email ? ' · ' + c.email : '' }}</option></select></label></div>
    <div><label class="form-field">Поиск опубликованного теста<input v-model="searches.test" placeholder="Название теста" maxlength="200" /></label><button type="button" class="text-button" @click="searchTests">Найти тест</button><label class="form-field">Тест<select v-model="form.test_version_id" aria-label="Тест приглашения" required><option value="" disabled>Выберите опубликованную версию</option><option v-for="t in tests" :key="t.version_id" :value="t.version_id">{{ t.title }} · версия {{ t.version_no }}</option></select></label></div>
    <div><label class="form-field">Поиск вакансии<input v-model="searches.vacancy" placeholder="Название позиции" maxlength="200" /></label><button type="button" class="text-button" @click="searchVacancies">Найти вакансию</button><label class="form-field">Вакансия<select v-model="form.vacancy_id" aria-label="Вакансия приглашения"><option value="">Без вакансии</option><option v-for="v in vacancies" :key="v.id" :value="v.id">{{ v.title }}</option></select></label></div>
    <label class="form-field">Действует до (МСК)<input v-model="form.expires_at" type="datetime-local" required /></label><label class="form-field">Допустимое число попыток<input v-model.number="form.max_attempts" type="number" min="1" max="20" required /></label><div class="form-actions"><button class="primary-link" :disabled="busy"><Plus :size="17" />{{ busy ? 'Выдаём…' : 'Выдать приглашение' }}</button></div>
  </form><p class="field-hint">В списках первые 20 совпадений. Уточните поиск, если нужной записи нет. Прохождение теста будет добавлено на следующем этапе.</p></section>
  <div class="question-toolbar"><h2>История приглашений</h2><select v-model="status" aria-label="Статус приглашений"><option value="">Все статусы</option><option value="issued">Выданные</option><option value="revoked">Отозванные</option><option value="expired">Истёкшие</option><option value="completed">Завершённые</option></select></div>
  <p v-if="loading" class="loading-state" role="status">Загружаем историю…</p><p v-else-if="!items.length" class="muted-text">Приглашений пока нет.</p>
  <section class="question-list"><article v-for="i in items" :key="i.id" class="invitation-row"><div><div class="question-labels"><span class="status-pill" :class="i.status === 'issued' ? 'active' : 'archived'">{{ labels[i.status] }}</span><span>До {{ formatDate(i.expires_at) }}</span></div><h3>{{ i.candidate_name }}</h3><p>{{ i.test_title }} · версия {{ i.test_version_no }}</p><p>{{ i.vacancy_title || 'Без вакансии' }} · попыток {{ i.used_attempts }} из {{ i.max_attempts }}</p></div><button v-if="i.status === 'issued'" class="secondary-button danger" :disabled="busy" @click="revoke(i)">Отозвать</button></article></section>
  <div v-if="total" class="pagination"><span>Всего: {{ total }}</span><div><button class="secondary-button" :disabled="page <= 1" @click="page--; load()">Назад</button><span>{{ page }}</span><button class="secondary-button" :disabled="page * 20 >= total" @click="page++; load()">Далее</button></div></div>
</template>
