<script setup lang="ts">
import { ref, shallowRef } from 'vue'
import { api } from './session'
interface Preview { valid: boolean; source_sha256: string; question_count: number; test_count: number; categories: string[]; tests: { source_id: string; title: string; question_count: number; minutes: number; passing: number; shuffle: boolean; published: boolean; show_score: boolean }[]; errors: { path: string; code: string; message: string }[]; warnings: string[]; already_applied: boolean }
interface Receipt { id: string; questions: { source_id: string; id: string }[]; tests: { source_id: string; id: string }[]; categories_created: number }
const source = shallowRef<string>(), report = ref<Preview>(), receipt = ref<Receipt>(), busy = ref(false), error = ref(''), fileName = ref(''), confirmation = ref('')
let generation = 0
async function choose(event: Event) {
  const current = ++generation
  source.value = undefined; report.value = undefined; receipt.value = undefined; confirmation.value = ''; error.value = ''; fileName.value = ''
  const file = (event.target as HTMLInputElement).files?.[0]
  if (!file) return
  if (file.size > 500 * 1024) { error.value = 'Размер JSON — не более 500 КиБ'; return }
  try { const data = (await file.text()).replace(/^\uFEFF/, ''); JSON.parse(data); if (current === generation) { source.value = data; fileName.value = file.name } }
  catch { if (current === generation) error.value = 'Не удалось прочитать JSON. Проверьте формат файла' }
}
async function preview() {
  busy.value = true; error.value = ''; report.value = undefined; confirmation.value = ''; receipt.value = undefined
  try { report.value = await api<Preview>('/import/questions/preview', { method: 'POST', body: '{"source":' + source.value + '}' }) }
  catch (err) { error.value = (err as Error).message }
  finally { busy.value = false }
}
async function apply() {
  if (!report.value?.valid || report.value.already_applied || confirmation.value !== 'ИМПОРТИРОВАТЬ') return
  busy.value = true; error.value = ''
  try { receipt.value = await api<Receipt>('/import/questions/apply', { method: 'POST', body: '{"source":' + source.value + ',"preview_sha256":' + JSON.stringify(report.value.source_sha256) + ',"confirmation":' + JSON.stringify(confirmation.value) + '}' }); confirmation.value = '' }
  catch (err) { error.value = (err as Error).message }
  finally { busy.value = false }
}
</script>
<template>
  <div class="page-heading"><div><p class="eyebrow">ПЕРЕНОС БАНКА</p><h1>Импорт CandidateTester</h1><p class="subtitle">Проверьте JSON и настройки перед переносом вопросов и тестов.</p></div><RouterLink to="/questions" class="secondary-button">К банку вопросов</RouterLink></div>
  <section class="editor-card import-card"><h2>1. Выберите файл</h2><p>CandidateTester version 1: до 200 вопросов типа single и 20 тестов. Размер — до 500 КиБ.</p><label class="form-field">JSON-файл<input type="file" accept=".json,application/json" :disabled="busy" @change="choose" /></label><p v-if="fileName" class="field-hint">{{ fileName }}</p><button class="primary-button" :disabled="source === undefined || busy" @click="preview">{{ busy ? 'Обрабатываем…' : 'Проверить файл' }}</button></section>
  <p v-if="error" class="form-error" role="alert">{{ error }}</p>
  <section v-if="report && !receipt" class="editor-card import-card" aria-label="Предварительный отчёт"><h2>2. Результат проверки</h2><p>Вопросов: <strong>{{ report.question_count }}</strong> · Тестов: <strong>{{ report.test_count }}</strong> · Категорий: <strong>{{ report.categories.length }}</strong></p><p class="field-hint">Проверка не добавляет вопросы и тесты в банк.</p>
    <template v-if="report.errors.length"><p class="form-error" role="alert">Исправьте ошибки до переноса. Показано не более 100 ошибок.</p><ul class="import-issues"><li v-for="(issue, i) in report.errors" :key="i"><code>{{ issue.path }}</code> — {{ issue.message }}</li></ul></template>
    <ul class="import-issues"><li v-for="warning in report.warnings" :key="warning">{{ warning }}</li></ul>
    <details v-if="report.categories.length"><summary>Категории</summary><ul><li v-for="category in report.categories" :key="category">{{ category }}</li></ul></details>
    <div v-for="t in report.tests" :key="t.source_id" class="import-test"><h3>{{ t.title }}</h3><p>{{ t.question_count }} вопросов · {{ t.minutes }} мин · Порог {{ t.passing }}%</p><p>{{ t.published ? 'Будет опубликован' : 'Останется черновиком' }} · {{ t.shuffle ? 'Вопросы перемешиваются' : 'Порядок вопросов сохранён' }}</p><p>{{ t.show_score ? 'Кандидат увидит итоговые баллы' : 'Баллы скрыты от кандидата' }}</p></div>
    <p v-if="report.already_applied" class="form-success" role="status">Этот файл уже импортирован в вашу организацию. Повторные записи не созданы.</p>
    <form v-else-if="report.valid" @submit.prevent="apply"><h2>3. Подтвердите перенос</h2><p>Будут созданы новые вопросы и тесты. Существующие объекты не перезаписываются. Тесты с признаком публикации станут доступны для новых приглашений.</p><label class="form-field">Введите ИМПОРТИРОВАТЬ<input v-model="confirmation" autocomplete="off" :disabled="busy" /></label><button class="primary-button" :disabled="busy || confirmation !== 'ИМПОРТИРОВАТЬ'">Импортировать в организацию</button></form>
  </section>
  <section v-if="receipt" class="editor-card import-card"><h2>Импорт завершён</h2><p class="form-success" role="status">Добавлено вопросов: {{ receipt.questions.length }}, тестов: {{ receipt.tests.length }}. Новых категорий: {{ receipt.categories_created }}.</p><RouterLink to="/questions" class="primary-link">Открыть вопросы</RouterLink><h3>Тесты</h3><ul><li v-for="t in receipt.tests" :key="t.id"><RouterLink :to="'/tests/' + t.id + '/edit'">{{ t.source_id }}</RouterLink></li></ul></section>
</template>
