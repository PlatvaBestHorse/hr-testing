<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api } from './session'
import QuestionImage from './QuestionImage.vue'
import ResultComments from './ResultComments.vue'
import { formatDate } from './domain'
import { type Report, resultStatusNames } from './result-domain'
import { downloadResult } from './result-download'
const route = useRoute(), report = ref<Report | null>(null), error = ref(''), loading = ref(false), exporting = ref(false)
let generation = 0
async function load() { const current = ++generation; loading.value = true; error.value = ''; report.value = null; try { const data = await api<Report>('/results/' + encodeURIComponent(String(route.params.id))); if (current === generation) report.value = data } catch (err) { if (current === generation) error.value = (err as Error).message } finally { if (current === generation) loading.value = false } }
async function exportFile(format: 'csv' | 'xlsx') { if (!report.value) return; exporting.value = true; error.value = ''; try { await downloadResult('/results/' + report.value.id + '/export?format=' + format, 'attempt-' + report.value.id + '.' + format) } catch (err) { error.value = (err as Error).message } finally { exporting.value = false } }
watch(() => route.params.id, load, { immediate: true })
</script>
<template>
  <div class="page-heading"><div><RouterLink to="/results" class="muted-text">← Все результаты</RouterLink><p class="eyebrow">РАЗБОР ПРОХОЖДЕНИЯ</p><h1>Отчёт по тесту</h1></div><div class="result-export-actions"><button class="secondary-button" :disabled="!report || exporting" @click="exportFile('csv')">Скачать ответы CSV</button><button class="secondary-button" :disabled="!report || exporting" @click="exportFile('xlsx')">Скачать отчёт XLSX</button></div></div>
  <p v-if="loading" role="status">Загружаем отчёт…</p><p v-if="error" class="form-error" role="alert">{{ error }}</p>
  <template v-if="report">
    <section class="result-card"><div><h2>{{ report.candidate_name }}</h2><p>{{ report.test_title }} · версия {{ report.test_version_no }} · попытка {{ report.attempt_no }}</p><p v-if="report.vacancy_title">{{ report.vacancy_title }}</p><p>{{ resultStatusNames[report.status] }}</p><p class="muted-text">Начало: {{ formatDate(report.started_at) }}<br />Завершение: {{ report.finished_at ? formatDate(report.finished_at) : '—' }}</p><p>С ответом: {{ report.answered }} из {{ report.questions.length }} · верных: {{ report.correct }}</p></div><div class="result-score"><strong>{{ report.score_percent }}%</strong><p>{{ report.score_points }} / {{ report.max_points }} баллов</p><p>Проходной процент: {{ report.pass_percent }}%</p><span>{{ report.passed ? 'Проходной балл достигнут' : 'Проходной балл не достигнут' }}</span></div></section>
    <section class="report-section" aria-label="Результаты по категориям"><h2>По категориям</h2><p v-if="!report.categories_frozen" class="muted-text">Для старых попыток категории взяты из текущего банка. Баллы и ответы сохранены по версии пройденного теста.</p><div class="report-categories"><article v-for="category in report.categories" :key="category.id + category.name" class="result-card"><div><h3>{{ category.name }}</h3><p>Верных: {{ category.correct }} из {{ category.questions }} · с ответом: {{ category.answered }}</p></div><div><strong>{{ category.score_percent }}%</strong><p>{{ category.score_points }} / {{ category.max_points }} баллов</p></div></article></div></section>
    <section class="report-section" aria-label="Разбор ответов"><h2>Ответы кандидата</h2><article v-for="question in report.questions" :key="question.id" class="report-question"><div class="passage-meta"><span>Вопрос {{ question.position }} · {{ question.category_name }} · версия {{ question.question_version_no }}</span><strong>{{ question.score_points }} / {{ question.max_points }} баллов</strong></div><p class="report-verdict" :class="{ correct: question.correct }">{{ question.correct ? 'Верно' : question.answered ? 'Неверно' : 'Нет ответа' }}</p><div v-if="question.passage" class="question-passage">{{ question.passage }}</div><h3>{{ question.body }}</h3><QuestionImage v-if="question.image_id" :src="'/api/v1/results/' + report.id + '/images/' + question.image_id" alt="Изображение вопроса" /><ul class="report-options"><li v-for="option in question.options" :key="option.id" :class="{ selected: option.selected, correct: option.is_correct }"><span>{{ option.label }}<QuestionImage v-if="option.image_id" :src="'/api/v1/results/' + report.id + '/images/' + option.image_id" :alt="'Изображение варианта: ' + option.label" /></span><strong v-if="option.selected">Выбран кандидатом</strong><span v-if="option.is_correct">Правильный вариант</span></li></ul><div v-if="question.explanation" class="report-explanation"><strong>Пояснение</strong><p>{{ question.explanation }}</p></div></article></section>
    <ResultComments :attempt-id="report.id" />
  </template>
</template>
