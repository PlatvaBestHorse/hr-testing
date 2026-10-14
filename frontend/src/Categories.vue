<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from './session'
interface Category { id: string; name: string; description: string }
const emit = defineEmits<{ changed: [] }>()
const items = ref<Category[]>([]), name = ref(''), description = ref(''), error = ref(''), busy = ref(false), editing = ref('')
async function load() { try { items.value = (await api<{ items: Category[] }>('/categories')).items } catch (err) { error.value = (err as Error).message } }
function edit(c: Category) { editing.value = c.id; name.value = c.name; description.value = c.description }
function reset() { editing.value = ''; name.value = ''; description.value = '' }
async function save() {
  busy.value = true; error.value = ''
  try { await api('/categories' + (editing.value ? '/' + editing.value : ''), { method: editing.value ? 'PATCH' : 'POST', body: JSON.stringify({ name: name.value, description: description.value }) }); reset(); await load(); emit('changed') }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
async function remove(c: Category) {
  if (!window.confirm('Удалить категорию «' + c.name + '»? Категории с вопросами удалить нельзя.')) return
  error.value = ''; busy.value = true
  try { await api('/categories/' + c.id, { method: 'DELETE' }); await load(); emit('changed') }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
onMounted(load)
</script>
<template>
  <section class="categories-panel"><div class="section-heading"><h2>Категории вопросов</h2><span class="small-label">{{ items.length }} КАТЕГОРИЙ</span></div>
    <form class="category-form" @submit.prevent="save"><label class="form-field">Название категории<input v-model="name" required maxlength="150" /></label><label class="form-field">Описание категории<input v-model="description" maxlength="2000" /></label><button class="primary-link" :disabled="busy">{{ editing ? 'Сохранить категорию' : 'Добавить категорию' }}</button><button v-if="editing" type="button" class="secondary-button" @click="reset">Отмена</button></form>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <ul class="category-list"><li v-for="c in items" :key="c.id"><span>{{ c.name }}</span><div><button class="text-button" :disabled="busy" @click="edit(c)">Изменить</button><button class="text-button danger" :disabled="busy" @click="remove(c)">Удалить</button></div></li></ul>
    <p v-if="items.length === 0" class="muted-text">Пока нет категорий. Вопрос можно создать и без категории.</p>
  </section>
</template>
