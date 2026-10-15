<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { api } from './session'
interface Document { revision: number; operator_name: string; contact: string; consent_text: string; policy_text: string; approved: boolean }
const form = reactive<Document>({ revision: 0, operator_name: '', contact: '', consent_text: '', policy_text: '', approved: false })
const busy = ref(false), error = ref(''), notice = ref('')
onMounted(async () => { try { Object.assign(form, await api<Document>('/privacy')) } catch (err) { error.value = (err as Error).message } })
async function save() {
  busy.value = true; error.value = ''; notice.value = ''
  try { Object.assign(form, await api<Document>('/privacy', { method: 'PATCH', body: JSON.stringify({ operator_name: form.operator_name, contact: form.contact, consent_text: form.consent_text, policy_text: form.policy_text, approved: form.approved, base_revision: form.revision }) })); notice.value = 'Новая версия документов сохранена. Кандидат увидит её перед следующим началом теста.' }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
</script>
<template>
  <form class="editor-card governance-policy" @submit.prevent="save"><h2>Согласие и политика обработки данных</h2><p>Тексты видны кандидату перед началом теста. Каждое сохранение создаёт новую версию; ранее подтверждённые версии сохраняются. Юридическое содержание утверждает организация.</p><p v-if="!form.approved" class="form-error">Документы не утверждены. Используйте только синтетические данные.</p><p v-if="error" class="form-error" role="alert">{{ error }}</p><p v-if="notice" class="form-success" role="status">{{ notice }}</p><label class="form-field">Наименование оператора данных<input v-model="form.operator_name" maxlength="250" :required="form.approved" :disabled="busy" /></label><label class="form-field">Рабочая почта по вопросам персональных данных<input v-model="form.contact" type="email" maxlength="320" :required="form.approved" :disabled="busy" /></label><label class="form-field">Текст согласия<textarea v-model="form.consent_text" required maxlength="20000" rows="8" :disabled="busy" /></label><label class="form-field">Политика обработки персональных данных<textarea v-model="form.policy_text" required maxlength="20000" rows="8" :disabled="busy" /></label><label class="check-field"><input v-model="form.approved" type="checkbox" :disabled="busy" />Организация утвердила эти тексты для использования</label><button class="primary-button" :disabled="busy">Сохранить новую версию документов</button><p class="muted-text">Версия: {{ form.revision }}. Укажите цели, состав данных, основание, сроки, получателей и порядок обращений/отзыва. Не включайте сведения конкретных кандидатов.</p></form>
</template>
