<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiError, forgetSession } from './session'
const route = useRoute(), router = useRouter(), reset = route.path === '/reset-password'
const token = ref(new URLSearchParams(route.hash.slice(1)).get('token') || '')
const email = ref(''), password = ref(''), repeated = ref(''), factor = ref(''), mfa = ref(false), local = ref(false), available = ref(false), busy = ref(false), done = ref(false), error = ref('')
onMounted(async () => { if (route.hash) await router.replace({ path: route.path, hash: '' }); try { const options = await api<{ recovery_available: boolean; local_mail: boolean }>('/auth/options'); local.value = options.local_mail; available.value = options.recovery_available } catch (err) { error.value = (err as Error).message } })
async function submit() {
  if (busy.value) return
  if (reset && password.value !== repeated.value) { error.value = 'Пароли не совпадают'; return }
  busy.value = true; error.value = ''
  try { await api('/auth/recovery/' + (reset ? 'complete' : 'request'), { method: 'POST', body: JSON.stringify(reset ? { token: token.value, new_password: password.value, second_factor: factor.value } : { email: email.value }) }); password.value = ''; repeated.value = ''; factor.value = ''; token.value = ''; done.value = true; if (reset) forgetSession(false) }
  catch (err) { if (err instanceof ApiError && err.code === 'mfa_required') mfa.value = true; error.value = (err as Error).message } finally { busy.value = false }
}
</script>
<template>
  <main class="login-page"><section class="login-card"><h1>{{ reset ? 'Новый пароль' : 'Восстановление доступа' }}</h1><p v-if="local" class="candidate-stage-note">Тестовая почта: письма сохраняются в закрытом локальном ящике и не отправляются на реальные адреса.</p>
    <p v-if="done" class="form-success" role="status">{{ reset ? 'Пароль изменён. Все прежние сессии завершены. Войдите с новым паролем.' : 'Если аккаунт доступен, письмо со ссылкой будет отправлено. Проверьте почту.' }}</p>
    <p v-else-if="reset && !token" class="form-error">Откройте ссылку из письма повторно. После обновления страницы её нужно открыть заново.</p>
    <form v-else-if="reset || available" @submit.prevent="submit"><template v-if="reset"><label class="form-field">Новый пароль<input v-model="password" type="password" autocomplete="new-password" minlength="12" maxlength="1024" required /></label><label class="form-field">Повтор нового пароля<input v-model="repeated" type="password" autocomplete="new-password" maxlength="1024" required /></label><label v-if="mfa" class="form-field">Код приложения или резервный код<input v-model="factor" autocomplete="one-time-code" maxlength="64" required /></label><p class="field-hint">Ссылка одноразовая. Смена пароля не отключает двухфакторную защиту.</p></template><label v-else class="form-field">Электронная почта<input v-model="email" type="email" autocomplete="username" maxlength="320" required /></label><button class="primary-link" :disabled="busy">{{ busy ? 'Подождите…' : reset ? 'Сохранить новый пароль' : 'Отправить ссылку' }}</button></form>
    <p v-else class="muted-text">Восстановление по почте не настроено. Обратитесь к администратору организации.</p><p v-if="error" class="form-error" role="alert">{{ error }}</p><RouterLink to="/login" class="back-link">Вернуться ко входу</RouterLink>
  </section></main>
</template>
