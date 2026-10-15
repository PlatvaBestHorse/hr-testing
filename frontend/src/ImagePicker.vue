<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { api } from './session'
import QuestionImage from './QuestionImage.vue'
const props = defineProps<{ modelValue: string; label: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [id: string]; busy: [value: boolean] }>()
const busy = ref(false), error = ref('')
let alive = true, controller: AbortController | undefined
onBeforeUnmount(() => { alive = false; controller?.abort(); if (busy.value) emit('busy', false) })
async function upload(event: Event) {
  const input = event.target as HTMLInputElement, file = input.files?.[0]
  if (!file || props.disabled || busy.value) return
  error.value = ''
  if (!['image/png', 'image/jpeg'].includes(file.type)) { error.value = 'Выберите PNG или JPEG'; input.value = ''; return }
  if (file.size > 5 * 1024 * 1024) { error.value = 'Максимальный размер — 5 МиБ'; input.value = ''; return }
  busy.value = true; emit('busy', true)
  controller = new AbortController()
  try { const data = new FormData(); data.append('file', file); const image = await api<{ id: string }>('/images', { method: 'POST', body: data, signal: controller.signal }); if (alive) emit('update:modelValue', image.id) }
  catch (err) { if (alive) error.value = (err as Error).message }
  finally { busy.value = false; if (alive) emit('busy', false); input.value = '' }
}
</script>
<template>
  <div class="image-picker">
    <label class="form-field">{{ label }}<input type="file" accept="image/png,image/jpeg" :disabled="disabled || busy" @change="upload" /></label>
    <p class="field-hint">PNG или JPEG, до 5 МиБ. Текст задания и варианты ответа остаются обязательными.</p>
    <p v-if="busy" class="muted-text" role="status">Загружаем изображение…</p><p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <template v-if="modelValue"><QuestionImage :src="'/api/v1/images/' + modelValue" :alt="label" /><button class="text-button danger" type="button" :disabled="disabled || busy" @click="emit('update:modelValue', '')">Убрать {{ label.toLowerCase() }}</button><p class="field-hint">Изменение сохранится в новой версии. Изображения предыдущих версий сохраняются.</p></template>
  </div>
</template>
