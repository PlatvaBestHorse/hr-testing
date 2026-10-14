<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from './session'
import { formatDate } from './domain'
import { type Result, resultStatusNames } from './result-domain'
import { downloadResult } from './result-download'
const items = ref<Result[]>([]), total = ref(0), page = ref(1), loading = ref(false), exporting = ref(false), error = ref('')
const search = ref(''), status = ref(''), passed = ref(''), from = ref(''), to = ref('')
let applied = new URLSearchParams(), generation = 0
async function load() {
  const current = ++generation; loading.value = true; error.value = ''
  try { const query = new URLSearchParams(applied); query.set('page', String(page.value)); const data = await api<{ items: Result[]; total: number }>('/results?' + query); if (current === generation) { items.value = data.items; total.value = data.total } }
  catch (err) { if (current === generation) { items.value = []; error.value = (err as Error).message } }
  finally { if (current === generation) loading.value = false }
}
function filter() { applied = new URLSearchParams({ search: search.value, status: status.value, passed: passed.value, date_from: from.value, date_to: to.value }); page.value = 1; void load() }
async function exportFile(format: 'csv' | 'xlsx') { exporting.value = true; error.value = ''; const query = new URLSearchParams(applied); query.set('format', format); try { await downloadResult('/results/export?' + query, 'results.' + format) } catch (err) { error.value = (err as Error).message } finally { exporting.value = false } }
onMounted(load)
</script>
<template>
  <div class="page-heading"><div><p class="eyebrow">ОЦЕНКА КАНДИДАТОВ</p><h1>Результаты тестов</h1><p class="subtitle">Сохранённые ответы, баллы и итоги прохождения.</p></div><button class="secondary-button" :disabled="loading" @click="load">Обновить</button></div>
  <form class="result-filters" @submit.prevent="filter">
    <label>Поиск<input v-model="search" type="search" aria-label="Поиск результатов" placeholder="Кандидат, тест или вакансия" /></label>
    <label>Статус<select v-model="status" aria-label="Статус результата"><option value="">Все статусы</option><option v-for="(name, key) in resultStatusNames" :key="key" :value="key">{{ name }}</option></select></label>
    <label>Итог<select v-model="passed" aria-label="Итог результата"><option value="">Все итоги</option><option value="true">Порог достигнут</option><option value="false">Порог не достигнут</option></select></label>
    <label>Начало с<input v-model="from" type="date" aria-label="Дата результатов с" /></label><label>Начало по<input v-model="to" type="date" aria-label="Дата результатов по" /></label><button class="primary-button" :disabled="loading">Применить фильтры</button>
  </form>
  <div class="result-export-actions"><p class="muted-text">Даты начала по МСК. Выгрузка всех страниц с применёнными фильтрами, до 10 000 строк.</p><button class="secondary-button" :disabled="exporting || loading" @click="exportFile('csv')">Скачать CSV</button><button class="secondary-button" :disabled="exporting || loading" @click="exportFile('xlsx')">Скачать XLSX</button></div>
  <p v-if="error" class="form-error" role="alert">{{ error }}</p><p v-if="loading" role="status">Загружаем результаты…</p><p v-if="exporting" role="status">Готовим файл…</p>
  <section v-if="!loading && !items.length" class="empty-state"><h2>Результатов не найдено</h2><p>Измените фильтры или пригласите кандидата на тест.</p></section>
  <section v-else-if="!loading" class="result-list" aria-label="Результаты кандидатов"><article v-for="item in items" :key="item.id" class="result-card"><div><h2>{{ item.candidate_name }}</h2><p>{{ item.test_title }} · версия {{ item.test_version_no }} · попытка {{ item.attempt_no }}</p><p v-if="item.vacancy_title" class="muted-text">{{ item.vacancy_title }}</p><p class="muted-text">{{ formatDate(item.started_at) }} · {{ resultStatusNames[item.status] }}</p><RouterLink v-if="['submitted', 'expired'].includes(item.status)" :to="'/results/' + item.id" class="primary-link">Открыть отчёт</RouterLink></div><div v-if="item.score_percent !== null" class="result-score"><strong>{{ item.score_percent }}%</strong><p>{{ item.score_points }} / {{ item.max_points }} баллов</p><span>{{ item.passed ? 'Проходной балл достигнут' : 'Проходной балл не достигнут' }}</span></div></article></section>
  <div class="passage-actions"><button class="secondary-button" :disabled="page <= 1 || loading" @click="page--; load()">Назад</button><span>Страница {{ page }} · всего {{ total }}</span><button class="secondary-button" :disabled="page * 20 >= total || loading" @click="page++; load()">Далее</button></div>
</template>
