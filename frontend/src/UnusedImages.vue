<script setup lang="ts">
import { ref } from 'vue'
import { api } from './session'
import QuestionImage from './QuestionImage.vue'
defineProps<{ excluded: string[]; disabled?: boolean }>()
const items = ref<{ id: string; byte_size: number; width: number; height: number }[]>([]), total = ref(0), page = ref(1), open = ref(false), busy = ref(false), error = ref('')
async function load(next = 1) { busy.value = true; error.value = ''; open.value = true; try { const data = await api<{ items: typeof items.value; total: number }>('/images?page=' + next); items.value = data.items; total.value = data.total; page.value = next } catch (err) { error.value = (err as Error).message } finally { busy.value = false } }
async function remove(id: string) { if (!window.confirm('Удалить неиспользуемое изображение?')) return; busy.value = true; error.value = ''; try { await api('/images/' + id, { method: 'DELETE' }); await load(page.value) } catch (err) { error.value = (err as Error).message } finally { busy.value = false } }
</script>
<template>
  <section class="editor-card"><h2>Неиспользуемые загрузки</h2><p class="muted-text">Здесь можно удалить файлы, которые не сохранены ни в одной версии вопроса. Лимит организации: 100 МиБ и 1000 изображений.</p><button type="button" class="secondary-button" :disabled="busy || disabled" @click="load()">Показать неиспользуемые изображения</button>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p><template v-if="open"><p class="muted-text">Найдено: {{ total }}</p><article v-for="item in items" :key="item.id" class="unused-image"><QuestionImage :src="'/api/v1/images/' + item.id" alt="Неиспользуемая загрузка" /><p class="field-hint">{{ item.width }} × {{ item.height }} · {{ Math.ceil(item.byte_size / 1024) }} КиБ</p><button type="button" class="text-button danger" :disabled="busy || disabled || excluded.includes(item.id)" @click="remove(item.id)">{{ excluded.includes(item.id) ? 'Используется в открытом редакторе' : 'Удалить загрузку' }}</button></article><div class="image-pages"><button type="button" :disabled="busy || page <= 1" @click="load(page - 1)">Назад</button><span>{{ page }}</span><button type="button" :disabled="busy || page * 20 >= total" @click="load(page + 1)">Далее</button></div></template>
  </section>
</template>
