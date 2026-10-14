<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { Library, Plus, Search, ArrowRight, Tags } from '@lucide/vue'
import { api } from './session'
import Categories from './Categories.vue'
interface Category { id: string; name: string; description: string }
interface Item { id: string; body: string; type: string; category_name: string; status: string; version_no: number; difficulty: number }
const types: Record<string, string> = { single_choice: 'Один ответ', multiple_choice: 'Несколько ответов', verbal_tfu: 'Верно / Неверно / Недостаточно информации' }
const statuses: Record<string, string> = { draft: 'Черновик', active: 'Опубликован', archived: 'В архиве' }
const items = ref<Item[]>([]), categories = ref<Category[]>([]), search = ref(''), categoryId = ref(''), status = ref(''), page = ref(1), total = ref(0), error = ref(''), loading = ref(true), showCategories = ref(false)
let generation = 0
async function load() {
  const current = ++generation; loading.value = true; error.value = ''
  try {
    const query = new URLSearchParams({ search: search.value, category_id: categoryId.value, status: status.value, page: String(page.value) })
    const response = await api<{ items: Item[]; total: number }>('/questions?' + query)
    if (current === generation) { items.value = response.items; total.value = response.total }
  } catch (err) { if (current === generation) error.value = (err as Error).message }
  finally { if (current === generation) loading.value = false }
}
async function loadCategories() { try { categories.value = (await api<{ items: Category[] }>('/categories')).items } catch (err) { error.value = (err as Error).message } }
function applyFilters() { page.value = 1; load() }
watch([categoryId, status], applyFilters)
onMounted(() => { load(); loadCategories() })
</script>
<template>
  <div class="page-heading"><div><p class="eyebrow">БАНК ВОПРОСОВ</p><h1>Вопросы для сильной команды</h1><p class="subtitle">Категории, правильные ответы и история каждой редакции.</p></div><RouterLink to="/questions/new" class="primary-link"><Plus :size="18" />Добавить вопрос</RouterLink></div>
  <div class="question-toolbar">
    <form class="search-field" @submit.prevent="applyFilters"><Search :size="18" /><input v-model="search" aria-label="Поиск вопросов" maxlength="200" placeholder="Найти вопрос…" /><button class="text-button" type="submit">Найти</button></form>
    <select v-model="categoryId" aria-label="Фильтр по категории"><option value="">Все категории</option><option v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</option></select>
    <select v-model="status" aria-label="Фильтр по статусу"><option value="">Все статусы</option><option value="draft">Черновики</option><option value="active">Опубликованные</option><option value="archived">Архив</option></select>
    <button class="secondary-button" @click="showCategories = !showCategories"><Tags :size="17" />Категории</button>
  </div>
  <Categories v-if="showCategories" @changed="loadCategories" />
  <p v-if="error" class="form-error" role="alert">{{ error }} <button class="text-button" @click="load">Повторить</button></p>
  <p v-if="loading" class="loading-state" role="status">Загружаем вопросы…</p>
  <section v-else-if="!error && items.length === 0" class="empty-state"><div class="empty-icon"><Library :size="32" /></div><h2>{{ total === 0 && !search && !categoryId && !status ? 'Добавьте первый вопрос' : 'По этим фильтрам вопросов нет' }}</h2><p>Выберите тип задания, добавьте варианты и отметьте правильные ответы.</p><RouterLink to="/questions/new" class="primary-link">Создать вопрос <Plus :size="18" /></RouterLink></section>
  <section v-else-if="!error" class="question-list" aria-label="Список вопросов"><RouterLink v-for="q in items" :key="q.id" :to="'/questions/' + q.id" class="question-row"><div class="question-row-main"><div class="question-labels"><span class="status-pill" :class="q.status">{{ statuses[q.status] }}</span><span>{{ q.category_name || 'Без категории' }}</span><span>Версия {{ q.version_no }}</span></div><h2>{{ q.body }}</h2><p>{{ types[q.type] }} <span>·</span> Сложность {{ q.difficulty }} из 5</p></div><ArrowRight :size="20" /></RouterLink></section>
  <div v-if="!loading && !error && total > 0" class="pagination"><span>Всего вопросов: {{ total }}</span><div><button class="secondary-button" :disabled="page <= 1" @click="page--; load()">Назад</button><span>Страница {{ page }}</span><button class="secondary-button" :disabled="page * 20 >= total" @click="page++; load()">Далее</button></div></div>
</template>
