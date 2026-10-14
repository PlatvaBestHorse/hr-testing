<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, ArrowRight, Plus, Search, Users, BriefcaseBusiness } from '@lucide/vue'
import { api, session } from './session'
import { formatDate, type List } from './domain'
const props = defineProps<{ kind: 'candidates' | 'vacancies' }>()
interface Entry { id: string; status: string; erased_at?: string | null; retention_hold?: boolean; full_name?: string; email?: string; phone?: string; title?: string; department?: string; assessor_ids?: string[] }
const route = useRoute(), router = useRouter(), isCandidate = computed(() => props.kind === 'candidates'), base = computed(() => '/' + props.kind), isList = computed(() => !route.params.id && !route.path.endsWith('/new')), isNew = computed(() => !route.params.id), editable = computed(() => session.user?.role !== 'assessor' && !(isCandidate.value && saved.value?.erased_at))
const items = ref<Entry[]>([]), assessors = ref<{ id: string; display_name: string }[]>([]), page = ref(1), total = ref(0), search = ref(''), filterStatus = ref(''), error = ref(''), notice = ref(''), loading = ref(false), busy = ref(false), saved = ref<Entry | null>(null)
const form = reactive({ full_name: '', email: '', phone: '', title: '', department: '', status: 'active', assessor_ids: [] as string[] })
let generation = 0
function title(e: Entry) { return e.full_name || e.title || '' }
async function load() {
  const current = ++generation; loading.value = true; error.value = ''
  try {
    if (isList.value) { const data = await api<List<Entry>>(base.value + '?' + new URLSearchParams({ search: search.value, status: filterStatus.value, page: String(page.value) })); if (current === generation) { items.value = data.items; total.value = data.total } }
    else if (isNew.value) { saved.value = null; Object.assign(form, { full_name: '', email: '', phone: '', title: '', department: '', status: isCandidate.value ? 'active' : 'open', assessor_ids: [] }) }
    else { const data = await api<Entry>(base.value + '/' + route.params.id); if (current !== generation) return; saved.value = data; Object.assign(form, { full_name: data.full_name || '', email: data.email || '', phone: data.phone || '', title: data.title || '', department: data.department || '', status: data.status, assessor_ids: data.assessor_ids || [] }) }
  } catch (err) { if (current === generation) error.value = (err as Error).message } finally { if (current === generation) loading.value = false }
}
async function loadAssessors() { if (isCandidate.value || !editable.value) return; try { assessors.value = (await api<{ items: typeof assessors.value }>('/assessors')).items } catch (err) { error.value = (err as Error).message } }
function filter() { page.value = 1; load() }
async function save() {
  if (!editable.value) return
  if (isCandidate.value && saved.value?.status === 'active' && form.status === 'archived' && !window.confirm('Архивировать кандидата? Его приглашения станут недоступны.')) return
  busy.value = true; error.value = ''; notice.value = ''
  const body = isCandidate.value ? { full_name: form.full_name, email: form.email, phone: form.phone, status: form.status } : { title: form.title, department: form.department, status: form.status, assessor_ids: form.assessor_ids }
  try { const data = await api<Entry>(base.value + (isNew.value ? '' : '/' + route.params.id), { method: isNew.value ? 'POST' : 'PATCH', body: JSON.stringify(body) }); if (isNew.value) await router.replace(base.value + '/' + data.id); else { saved.value = data; await load() }; notice.value = isCandidate.value ? 'Карточка кандидата сохранена' : 'Карточка вакансии сохранена' }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
watch(() => [props.kind, route.params.id, isList.value], () => { page.value = 1; filterStatus.value = ''; search.value = ''; load(); loadAssessors() }, { immediate: true })
watch(filterStatus, filter)
</script>
<template>
  <template v-if="isList">
    <div class="page-heading"><div><p class="eyebrow">{{ isCandidate ? 'КАРТОЧКИ КАНДИДАТОВ' : 'НАБОР КОМАНДЫ' }}</p><h1>{{ isCandidate ? 'Кандидаты' : 'Вакансии' }}</h1><p class="subtitle">{{ editable ? isCandidate ? 'Контакты и приглашения — в одном месте.' : 'Открытые позиции и назначенные оценщики.' : 'Только записи назначенных вам вакансий.' }}</p></div><RouterLink v-if="editable" :to="base + '/new'" class="primary-link"><Plus :size="18" />{{ isCandidate ? 'Добавить кандидата' : 'Создать вакансию' }}</RouterLink></div>
    <div class="question-toolbar"><form class="search-field" @submit.prevent="filter"><Search :size="18" /><input v-model="search" :aria-label="isCandidate ? 'Поиск кандидатов' : 'Поиск вакансий'" placeholder="Поиск…" maxlength="200" /><button class="text-button">Найти</button></form><select v-model="filterStatus" aria-label="Фильтр статуса"><option value="">Все статусы</option><template v-if="isCandidate"><option value="active">Активные</option><option value="archived">Архив</option></template><template v-else><option value="open">Открытые</option><option value="closed">Закрытые</option><option value="archived">Архив</option></template></select></div>
  </template>
  <template v-else><RouterLink :to="base" class="back-link"><ArrowLeft :size="17" />{{ isCandidate ? 'К кандидатам' : 'К вакансиям' }}</RouterLink><div class="page-heading"><div><p class="eyebrow">{{ editable ? 'КАРТОЧКА' : 'ПРОСМОТР' }}</p><h1>{{ isCandidate ? isNew ? 'Новый кандидат' : 'Карточка кандидата' : isNew ? 'Новая вакансия' : 'Карточка вакансии' }}</h1><p class="subtitle">{{ isCandidate ? 'Укажите данные и пригласите на опубликованный тест.' : 'Укажите позицию и сотрудников, которые будут оценивать результаты.' }}</p></div></div></template>
  <p v-if="error" class="form-error" role="alert">{{ error }}</p><p v-if="notice" class="form-success" role="status">{{ notice }}</p><p v-if="loading" class="loading-state" role="status">Загружаем записи…</p>
  <template v-else-if="!error || !isList && (isNew || saved)">
    <template v-if="isList"><section v-if="!items.length" class="empty-state"><div class="empty-icon"><component :is="isCandidate ? Users : BriefcaseBusiness" :size="32" /></div><h2>{{ isCandidate ? 'Кандидатов пока нет' : 'Вакансий пока нет' }}</h2><p>{{ editable ? 'Добавьте первую запись или измените фильтры.' : 'Записи появятся, когда HR назначит вас оценщиком.' }}</p></section><section v-else class="question-list"><RouterLink v-for="item in items" :key="item.id" :to="base + '/' + item.id" class="question-row"><div class="question-row-main"><div class="question-labels"><span class="status-pill" :class="['active', 'open'].includes(item.status) ? 'active' : 'archived'">{{ item.status === 'active' ? 'Активен' : item.status === 'open' ? 'Открыта' : item.status === 'closed' ? 'Закрыта' : 'В архиве' }}</span></div><h2>{{ title(item) }}</h2><p>{{ isCandidate ? item.email || item.phone || 'Контакты не указаны' : item.department || 'Подразделение не указано' }}</p></div><ArrowRight :size="19" /></RouterLink></section><div v-if="total" class="pagination"><span>Всего: {{ total }}</span><div><button class="secondary-button" :disabled="page <= 1" @click="page--; load()">Назад</button><span>{{ page }}</span><button class="secondary-button" :disabled="page * 20 >= total" @click="page++; load()">Далее</button></div></div></template>
    <p v-if="!isList && isCandidate && saved?.erased_at" class="muted-text">Контактные данные удалены {{ formatDate(saved.erased_at) }}. Редактирование и новые приглашения закрыты.</p><form v-if="!isList" class="directory-form editor-card" @submit.prevent="save">
      <template v-if="isCandidate"><label class="form-field">ФИО кандидата<input v-model="form.full_name" required maxlength="250" :disabled="!editable" /></label><div class="form-grid"><label class="form-field">Почта кандидата<input v-model="form.email" type="email" maxlength="320" :disabled="!editable" /></label><label class="form-field">Телефон кандидата<input v-model="form.phone" type="tel" maxlength="40" :disabled="!editable" /></label></div></template>
      <template v-else><label class="form-field">Название вакансии<input v-model="form.title" required maxlength="250" :disabled="!editable" /></label><label class="form-field">Подразделение<input v-model="form.department" maxlength="200" :disabled="!editable" /></label><section v-if="editable"><h2>Оценщики</h2><p class="muted-text">Оценщик видит только назначенные ему вакансии и связанных кандидатов.</p><label v-for="m in assessors" :key="m.id" class="check-field"><input v-model="form.assessor_ids" type="checkbox" :value="m.id" />{{ m.display_name }}</label><p v-if="!assessors.length" class="muted-text">Нет активных оценщиков. Администратор может добавить их в разделе «Команда».</p></section></template>
      <label class="form-field">Статус<select v-model="form.status" aria-label="Статус карточки" :disabled="!editable"><template v-if="isCandidate"><option value="active">Активен</option><option value="archived">В архиве</option></template><template v-else><option value="open">Открыта</option><option value="closed">Закрыта</option><option value="archived">В архиве</option></template></select></label>
      <p v-if="isCandidate && saved?.retention_hold" class="muted-text">Установлен запрет очистки контактных данных.</p><RouterLink v-if="isCandidate && saved && !saved.erased_at && session.user?.role === 'org_admin'" :to="'/retention?candidate_id=' + saved.id" class="back-link">Управлять хранением данных кандидата</RouterLink><div class="form-actions"><button v-if="editable" class="primary-link" :disabled="busy">{{ busy ? 'Сохраняем…' : isCandidate ? 'Сохранить кандидата' : 'Сохранить вакансию' }}</button><RouterLink v-if="isCandidate && saved && editable && saved.status === 'active'" :to="'/invitations?candidate_id=' + saved.id" class="secondary-button">Пригласить на тест</RouterLink></div>
      <p v-if="isCandidate && saved" class="muted-text">Результаты прохождений доступны в разделе «Результаты».</p>
    </form>
  </template>
</template>
