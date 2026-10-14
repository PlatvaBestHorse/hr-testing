<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { ClipboardList, Plus, ArrowRight, Search } from '@lucide/vue'
import { api } from './session'
import type { List, TestRecord } from './domain'
const items = ref<TestRecord[]>([]), search = ref(''), status = ref(''), page = ref(1), total = ref(0), error = ref(''), loading = ref(true)
let generation = 0
async function load() {
  const current = ++generation; error.value = ''; loading.value = true
  try { const data = await api<List<TestRecord>>('/tests?' + new URLSearchParams({ search: search.value, status: status.value, page: String(page.value) })); if (current === generation) { items.value = data.items; total.value = data.total } }
  catch (err) { if (current === generation) error.value = (err as Error).message } finally { if (current === generation) loading.value = false }
}
function filter() { page.value = 1; load() }
watch(status, filter); onMounted(load)
</script>
<template>
  <div class="page-heading"><div><p class="eyebrow">КОНСТРУКТОР ТЕСТОВ</p><h1>Тесты для вашей команды</h1><p class="subtitle">Состав, время и правила оценки — в каждой сохранённой версии.</p></div><RouterLink to="/tests/new" class="primary-link"><Plus :size="18" />Создать тест</RouterLink></div>
  <div class="question-toolbar"><form class="search-field" @submit.prevent="filter"><Search :size="18" /><input v-model="search" aria-label="Поиск тестов" placeholder="Найти тест…" maxlength="200" /><button class="text-button">Найти</button></form><select v-model="status" aria-label="Статус тестов"><option value="">Все статусы</option><option value="draft">Черновики</option><option value="published">Опубликованные</option><option value="archived">Архив</option></select></div>
  <p v-if="error" class="form-error" role="alert">{{ error }} <button class="text-button" @click="load">Повторить</button></p><p v-if="loading" class="loading-state" role="status">Загружаем тесты…</p>
  <section v-else-if="!error && !items.length" class="empty-state"><div class="empty-icon"><ClipboardList :size="32" /></div><h2>{{ search || status ? 'По этим фильтрам тестов нет' : 'Соберите первый тест' }}</h2><p>Выберите опубликованные вопросы и задайте параметры прохождения.</p><RouterLink to="/tests/new" class="primary-link">Создать тест <Plus :size="18" /></RouterLink></section>
  <section v-else-if="!error" class="question-list"><RouterLink v-for="t in items" :key="t.id" :to="'/tests/' + t.id + '/edit'" class="question-row"><div class="question-row-main"><div class="question-labels"><span class="status-pill" :class="t.status === 'archived' ? 'archived' : t.version_status === 'published' ? 'active' : 'draft'">{{ t.status === 'archived' ? 'В архиве' : t.version_status === 'published' ? 'Опубликован' : 'Черновик' }}</span><span>Версия {{ t.version_no }}</span></div><h2>{{ t.title }}</h2><p>{{ t.question_count }} вопросов · {{ Math.round(t.duration_seconds / 60) }} мин · порог {{ t.pass_percent }}%</p></div><ArrowRight :size="20" /></RouterLink></section>
  <div v-if="total && !loading && !error" class="pagination"><span>Всего тестов: {{ total }}</span><div><button class="secondary-button" :disabled="page <= 1" @click="page--; load()">Назад</button><span>{{ page }}</span><button class="secondary-button" :disabled="page * 20 >= total" @click="page++; load()">Далее</button></div></div>
</template>
