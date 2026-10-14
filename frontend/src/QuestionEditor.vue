<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Eye, Save, CircleCheck, Trash2, Plus } from '@lucide/vue'
import { api } from './session'
interface Option { label: string; is_correct: boolean }
interface Question { id: string; category_id: string; difficulty: number; type: string; body: string; passage: string; explanation: string; status: string; version_status: string; version_no: number; latest_version_no: number; options: Option[]; versions: { version_no: number; status: string }[] }
const route = useRoute(), router = useRouter()
const isNew = computed(() => !route.params.id)
const form = reactive({ category_id: '', difficulty: 2, type: 'single_choice', body: '', passage: '', explanation: '', options: [{ label: '', is_correct: false }, { label: '', is_correct: false }] as Option[] })
const categories = ref<{ id: string; name: string }[]>([]), question = ref<Question | null>(null), error = ref(''), notice = ref(''), loading = ref(false), busy = ref(false), preview = ref(false), selectedVersion = ref(0), baseline = ref('')
const isArchived = computed(() => question.value?.status === 'archived')
function payload() { return { ...form, options: form.options.map(o => ({ label: o.label, is_correct: o.is_correct })), base_version_no: question.value?.latest_version_no || 0 } }
const dirty = computed(() => JSON.stringify(payload()) !== baseline.value)
onBeforeRouteLeave(() => !dirty.value || busy.value || window.confirm('У вас есть несохранённые изменения. Покинуть редактор?'))
let generation = 0
async function load(version = 0) {
  if (isNew.value) {
    generation++; question.value = null; error.value = ''; notice.value = ''; loading.value = false
    Object.assign(form, { category_id: '', difficulty: 2, type: 'single_choice', body: '', passage: '', explanation: '', options: [{ label: '', is_correct: false }, { label: '', is_correct: false }] })
    baseline.value = JSON.stringify(payload()); return
  }
  const current = ++generation; loading.value = true; error.value = ''
  try {
    const q = await api<Question>('/questions/' + route.params.id + (version ? '?version_no=' + version : ''))
    if (current !== generation) return
    question.value = q; selectedVersion.value = q.version_no
    Object.assign(form, { category_id: q.category_id, difficulty: q.difficulty, type: q.type, body: q.body, passage: q.passage, explanation: q.explanation, options: q.options.map(o => ({ label: o.label, is_correct: o.is_correct })) })
    baseline.value = JSON.stringify(payload())
  } catch (err) { if (current === generation) error.value = (err as Error).message } finally { if (current === generation) loading.value = false }
}
function changeType() {
  if (form.type === 'verbal_tfu') form.options = ['Верно', 'Неверно', 'Недостаточно информации'].map(label => ({ label, is_correct: false }))
  else if (form.type === 'single_choice') { const first = form.options.findIndex(o => o.is_correct); form.options.forEach((o, i) => { o.is_correct = i === first }) }
}
function choose(index: number, checked: boolean) {
  if (form.type !== 'multiple_choice') form.options.forEach(o => { o.is_correct = false })
  form.options[index]!.is_correct = checked
}
async function save() {
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const saved = await api<{ id: string; version_no: number }>('/questions' + (isNew.value ? '' : '/' + route.params.id + '/versions'), { method: 'POST', body: JSON.stringify(payload()) })
    if (isNew.value) await router.replace('/questions/' + saved.id)
    else await load()
    notice.value = 'Версия ' + saved.version_no + ' сохранена. Теперь её можно опубликовать.'
  } catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
async function publish() {
  if (!question.value || dirty.value || selectedVersion.value !== question.value.latest_version_no) return
  busy.value = true; error.value = ''; notice.value = ''
  try { await api('/questions/' + route.params.id + '/publish', { method: 'POST', body: JSON.stringify({ base_version_no: question.value.latest_version_no }) }); await load(); notice.value = 'Вопрос опубликован. Эта версия сохранится без изменений.' }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
async function archive() {
  if (!window.confirm('Переместить вопрос в архив? История версий сохранится.')) return
  busy.value = true; error.value = ''
  try { await api('/questions/' + route.params.id + '/archive', { method: 'POST' }); await router.push('/questions') }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
function selectVersion() {
  if (dirty.value && !window.confirm('Несохранённые изменения будут потеряны. Открыть другую версию?')) { selectedVersion.value = question.value!.version_no; return }
  notice.value = ''; load(selectedVersion.value)
}
watch(() => route.params.id, () => load())
onMounted(async () => { load(); try { categories.value = (await api<{ items: typeof categories.value }>('/categories')).items } catch (err) { error.value = (err as Error).message } })
</script>
<template>
  <RouterLink to="/questions" class="back-link"><ArrowLeft :size="17" />К банку вопросов</RouterLink>
  <div class="page-heading"><div><p class="eyebrow">РЕДАКТОР ВОПРОСА</p><h1>{{ isNew ? 'Новый вопрос' : 'Редактирование вопроса' }}</h1><p class="subtitle">{{ isNew ? 'Хорошее задание начинается с понятной формулировки.' : 'Каждое сохранение создаёт новую версию. История остаётся доступной.' }}</p></div><button class="secondary-button" :disabled="loading || !!error && !form.body" @click="preview = !preview"><Eye :size="18" />{{ preview ? 'Скрыть предпросмотр' : 'Предпросмотр' }}</button></div>
  <p v-if="error" class="form-error" role="alert">{{ error }}</p><p v-if="notice" class="form-success" role="status">{{ notice }}</p><p v-if="loading" class="loading-state" role="status">Загружаем вопрос…</p>
  <template v-else>
    <div v-if="question" class="version-strip"><span class="status-pill" :class="question.status">{{ isArchived ? 'В архиве' : question.version_status === 'published' ? 'Опубликована' : 'Черновик' }}</span><label>Версия <select v-model="selectedVersion" aria-label="Версия вопроса" @change="selectVersion"><option v-for="v in question.versions" :key="v.version_no" :value="v.version_no">{{ v.version_no }} — {{ v.status === 'published' ? 'опубликована' : 'черновик' }}</option></select></label><span v-if="selectedVersion !== question.latest_version_no">Просмотр прошлой версии. Сохранение создаст новую.</span></div>
    <form class="editor-layout" @submit.prevent="save"><div class="editor-main"><section class="editor-card"><h2>Содержание</h2><div class="form-grid"><label class="form-field">Тип вопроса<select v-model="form.type" :disabled="isArchived" @change="changeType"><option value="single_choice">Один правильный ответ</option><option value="multiple_choice">Несколько правильных ответов</option><option value="verbal_tfu">Верно / Неверно / Недостаточно информации</option></select></label><label class="form-field">Категория<select v-model="form.category_id" :disabled="isArchived"><option value="">Без категории</option><option v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</option></select></label></div><label class="form-field">Вводный текст <span class="field-hint">Необязательно. Например, отрывок для анализа.</span><textarea v-model="form.passage" rows="4" maxlength="20000" :disabled="isArchived" /></label><label class="form-field">Текст вопроса<textarea v-model="form.body" rows="4" maxlength="10000" required :disabled="isArchived" placeholder="Что должен определить кандидат?" /></label></section>
    <section class="editor-card"><div class="section-heading"><div><h2>Варианты ответа</h2><p>{{ form.type === 'multiple_choice' ? 'Отметьте минимум два правильных варианта.' : 'Отметьте ровно один правильный вариант.' }}</p></div></div><div v-for="(option, index) in form.options" :key="index" class="option-editor"><input :type="form.type === 'multiple_choice' ? 'checkbox' : 'radio'" name="correct-option" :checked="option.is_correct" :disabled="isArchived" :aria-label="'Правильный вариант ' + (index + 1)" @change="choose(index, ($event.target as HTMLInputElement).checked)" /><label class="form-field">Вариант {{ index + 1 }}<input v-model="option.label" required maxlength="2000" :readonly="form.type === 'verbal_tfu'" :disabled="isArchived" /></label><button v-if="form.type !== 'verbal_tfu'" class="icon-button danger" type="button" :disabled="isArchived || form.options.length <= 2" :aria-label="'Удалить вариант ' + (index + 1)" @click="form.options.splice(index, 1)"><Trash2 :size="17" /></button></div><button v-if="form.type !== 'verbal_tfu'" type="button" class="secondary-button" :disabled="isArchived || form.options.length >= 10" @click="form.options.push({ label: '', is_correct: false })"><Plus :size="17" />Добавить вариант</button></section>
    <section class="editor-card"><h2>Пояснение для HR</h2><p class="muted-text">Не показывается кандидату во время прохождения теста.</p><label class="form-field">Пояснение к ответу<textarea v-model="form.explanation" rows="3" maxlength="10000" :disabled="isArchived" /></label></section></div>
    <aside class="editor-side"><section class="editor-card"><h2>Параметры</h2><label class="form-field">Сложность<select v-model.number="form.difficulty" :disabled="isArchived"><option v-for="level in 5" :key="level" :value="level">{{ level }} из 5</option></select></label><p class="muted-text">Сначала сохраните вопрос, затем опубликуйте готовую версию.</p><button type="submit" class="primary-link" :disabled="busy || isArchived"><Save :size="17" />{{ busy ? 'Сохраняем…' : isNew ? 'Сохранить черновик' : 'Сохранить новую версию' }}</button><button v-if="!isNew && !isArchived" type="button" class="secondary-button" :disabled="busy || dirty || question?.version_status === 'published' || selectedVersion !== question?.latest_version_no" @click="publish"><CircleCheck :size="17" />Опубликовать</button><p v-if="!isNew && dirty" class="field-hint">Перед публикацией сохраните изменения.</p><button v-if="!isNew && !isArchived" type="button" class="text-button danger" :disabled="busy" @click="archive">Переместить в архив</button></section></aside></form>
    <section v-if="preview" class="preview-card"><p class="eyebrow">ПРЕДПРОСМОТР ДЛЯ КАНДИДАТА</p><p v-if="form.passage" class="question-passage">{{ form.passage }}</p><h2>{{ form.body || 'Здесь появится текст вопроса' }}</h2><label v-for="(option, index) in form.options" :key="index" class="preview-option"><input :type="form.type === 'multiple_choice' ? 'checkbox' : 'radio'" name="preview-answer" />{{ option.label || 'Вариант ответа' }}</label><p class="field-hint">Правильные ответы и пояснение в этом представлении скрыты.</p></section>
  </template>
</template>
