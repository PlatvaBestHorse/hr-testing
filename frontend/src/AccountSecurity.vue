<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { api, forgetSession } from './session'
const router = useRouter()
const enabled = ref(false), available = ref(false), remaining = ref(0), busy = ref(false), error = ref('')
const password = ref(''), newPassword = ref(''), repeated = ref(''), factor = ref(''), setupCode = ref(''), secret = ref(''), codes = ref<string[]>([])
async function load() { try { const s = await api<{ mfa_enabled: boolean; mfa_available: boolean; recovery_codes_remaining: number }>('/security'); enabled.value = s.mfa_enabled; available.value = s.mfa_available; remaining.value = s.recovery_codes_remaining } catch (err) { error.value = (err as Error).message } }
function signIn() { codes.value = []; secret.value = ''; password.value = ''; newPassword.value = ''; repeated.value = ''; factor.value = ''; forgetSession(); void router.replace('/login?security=updated') }
async function perform(action: string) {
  if (busy.value) return
  if (action === 'password' && (newPassword.value !== repeated.value || newPassword.value.length < 12)) { error.value = 'Повторите одинаковый новый пароль от 12 символов'; return }
  if (action === 'mfa/disable' && !window.confirm('Отключить двухфакторную защиту и завершить все сессии?')) return
  busy.value = true; error.value = ''
  try {
    if (action === 'password') { await api('/security/password', { method: 'POST', body: JSON.stringify({ current_password: password.value, new_password: newPassword.value, second_factor: factor.value }) }); signIn() }
    else if (action === 'mfa/setup') { const data = await api<{ secret: string }>('/security/mfa/setup', { method: 'POST', body: JSON.stringify({ password: password.value }) }); secret.value = data.secret }
    else {
      const data = await api<{ recovery_codes: string[] | null }>('/security/' + action, { method: 'POST', body: JSON.stringify(action === 'mfa/confirm' ? { password: password.value, code: setupCode.value } : { password: password.value, second_factor: factor.value }) })
      secret.value = ''; setupCode.value = ''; password.value = ''; factor.value = ''
      if (data.recovery_codes?.length) codes.value = data.recovery_codes; else signIn()
    }
  } catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
onBeforeRouteLeave(() => !codes.value.length || window.confirm('Резервные коды показаны один раз. Вы сохранили их?'))
onMounted(load)
</script>
<template>
  <div class="page-heading"><div><p class="eyebrow">ВАШ АККАУНТ</p><h1>Безопасность</h1><p class="subtitle">Пароль и двухфакторная защита действуют во всех ваших организациях.</p></div></div>
  <p v-if="error" class="form-error" role="alert">{{ error }}</p>
  <section v-if="codes.length" class="editor-card account-card"><h2>Сохраните резервные коды</h2><p>Каждый код можно использовать один раз вместо кода приложения. Сохраните их в защищённом месте отдельно от телефона. Старые резервные коды больше не действуют.</p><ul class="recovery-codes" aria-label="Резервные коды"><li v-for="code in codes" :key="code"><code>{{ code }}</code></li></ul><p>Все прежние сессии завершены. После сохранения кодов войдите снова.</p><button class="primary-button" @click="signIn">Коды сохранены — перейти ко входу</button></section>
  <div v-else class="account-grid">
    <section class="editor-card account-card"><h2>Подтвердите действие</h2><label class="form-field">Текущий пароль<input v-model="password" type="password" autocomplete="current-password" maxlength="1024" :disabled="busy" /></label><label v-if="enabled" class="form-field">Код приложения или резервный код<input v-model="factor" autocomplete="one-time-code" maxlength="64" :disabled="busy" /></label><p class="field-hint">Для изменения пароля или защиты потребуется повторное подтверждение.</p></section>
    <section class="editor-card account-card"><h2>Смена пароля</h2><form @submit.prevent="perform('password')"><label class="form-field">Новый пароль<input v-model="newPassword" type="password" autocomplete="new-password" minlength="12" maxlength="1024" required :disabled="busy" /></label><label class="form-field">Повтор нового пароля<input v-model="repeated" type="password" autocomplete="new-password" required maxlength="1024" :disabled="busy" /></label><p class="field-hint">От 12 символов. Все сессии, включая текущую, будут завершены.</p><button class="primary-button" :disabled="busy || !password">Изменить пароль</button></form></section>
    <section class="editor-card account-card"><h2>Двухфакторная защита</h2><p v-if="enabled">Включена. Осталось резервных кодов: {{ remaining }}.</p><p v-else>Добавьте второй фактор из приложения-аутентификатора. Особенно рекомендуется администраторам.</p>
      <template v-if="enabled"><button class="secondary-button" :disabled="busy || !password || !factor" @click="perform('mfa/recovery-codes')">Получить новые резервные коды</button><button class="text-button danger" :disabled="busy || !password || !factor" @click="perform('mfa/disable')">Отключить двухфакторную защиту</button></template>
      <template v-else-if="available"><button v-if="!secret" class="secondary-button" :disabled="busy || !password" @click="perform('mfa/setup')">Настроить приложение</button><template v-else><p>В приложении-аутентификаторе выберите ввод ключа вручную. Тип: по времени, 6 цифр, каждые 30 секунд.</p><label class="form-field">Ключ приложения<input :value="secret" readonly autocomplete="off" spellcheck="false" /></label><p class="field-hint">Не передавайте ключ другим людям. Он показывается только при настройке. Подтвердите в течение 10 минут.</p><form @submit.prevent="perform('mfa/confirm')"><label class="form-field">Код подтверждения<input v-model="setupCode" autocomplete="one-time-code" inputmode="numeric" pattern="[0-9]{6}" required maxlength="6" :disabled="busy" /></label><button class="primary-button" :disabled="busy || !password">Включить защиту</button></form></template></template>
      <p v-else class="form-error">Двухфакторная защита временно недоступна. Обратитесь к оператору.</p>
    </section>
  </div>
</template>
