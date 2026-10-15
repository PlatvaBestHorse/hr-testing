<script setup lang="ts">
import { ref, watch } from 'vue'
import { api } from './session'
import { formatDate } from './domain'
const props = defineProps<{ candidateId: string }>()
const emit = defineEmits<{ erased: [] }>()
interface Preview { id: string; full_name: string; invitations: number; attempts: number; answers: number; comments: number; active_attempts: number; retention_hold: boolean; expires_at: string }
const preview = ref<Preview | null>(null), phrase = ref(''), busy = ref(false), error = ref('')
watch(() => props.candidateId, () => { preview.value = null; phrase.value = ''; error.value = '' })
async function prepare() {
  busy.value = true; error.value = ''; preview.value = null; phrase.value = ''
  try { preview.value = await api<Preview>('/candidates/' + props.candidateId + '/erasure/preview', { method: 'POST', body: '{}' }) }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
async function apply() {
  if (!preview.value || preview.value.retention_hold || phrase.value !== 'УДАЛИТЬ КАНДИДАТА') return
  busy.value = true; error.value = ''
  try { await api('/erasure/previews/' + preview.value.id + '/apply', { method: 'POST', body: JSON.stringify({ confirmation: phrase.value }) }); emit('erased') }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
</script>
<template>
  <section class="editor-card governance-policy"><h2>Полное удаление кандидата</h2><p>Удаляются карточка, все приглашения, прохождения, ответы, результаты, комментарии к ним, согласия и связанные сообщения. Активные прохождения прекратятся. В журнале останется факт удаления без имени и контактов.</p><p class="muted-text">Проверьте основание запроса и обязательства хранения. Общие вопросы и тесты сохранятся. Уже скачанные экспорты, письма и резервные копии требуют отдельной обработки по политике организации.</p><p v-if="error" role="alert" class="form-error">{{ error }}</p><button class="secondary-button" :disabled="busy" @click="prepare">Подготовить полное удаление</button>
    <template v-if="preview"><h3>{{ preview.full_name }}</h3><p>Приглашений: {{ preview.invitations }} · Прохождений: {{ preview.attempts }} · Ответов: {{ preview.answers }} · Комментариев: {{ preview.comments }}.</p><p v-if="preview.active_attempts" class="form-error">Активных прохождений: {{ preview.active_attempts }}. Их данные также будут удалены.</p><p class="muted-text">Предпросмотр действует до {{ formatDate(preview.expires_at) }}. После изменения данных потребуется новый.</p><p v-if="preview.retention_hold" class="form-error">Удаление заблокировано запретом хранения. Рассмотрите его основание в разделе «Хранение данных».</p><template v-else><label class="form-field">Для подтверждения введите УДАЛИТЬ КАНДИДАТА<input v-model="phrase" aria-label="Подтверждение полного удаления" autocomplete="off" :disabled="busy" /></label><button class="danger-button" :disabled="busy || phrase !== 'УДАЛИТЬ КАНДИДАТА'" @click="apply">Удалить кандидата и результаты</button></template></template>
  </section>
</template>
