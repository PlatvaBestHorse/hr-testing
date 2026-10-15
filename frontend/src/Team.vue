<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Users, Plus } from '@lucide/vue'
import { api, forgetSession, roleNames, session, type Role } from './session'
interface Member { id: string; email: string; display_name: string; role: Role; status: string }
const items = ref<Member[]>([]), error = ref(''), notice = ref(''), busy = ref(false)
const form = reactive({ display_name: '', email: '', password: '', role: 'recruiter' as Role })
const byInvitation = ref(true)
async function resend(member: Member) { busy.value = true; error.value = ''; try { await api('/team/' + member.id + '/invitation', { method: 'POST' }); notice.value = 'Новое приглашение подготовлено. Прежняя ссылка больше не действует.' } catch (err) { error.value = (err as Error).message } finally { busy.value = false } }
async function load() { try { items.value = (await api<{ items: Member[] }>('/team')).items } catch (err) { error.value = (err as Error).message } }
async function create() {
  busy.value = true; error.value = ''; notice.value = ''
  try { await api(byInvitation.value ? '/team/invitations' : '/team', { method: 'POST', body: JSON.stringify(byInvitation.value ? { display_name: form.display_name, email: form.email, role: form.role } : form) }); form.display_name = ''; form.email = ''; form.password = ''; notice.value = byInvitation.value ? 'Приглашение подготовлено к отправке. Сотрудник задаст пароль по ссылке в письме.' : 'Сотрудник добавлен. Передайте ему начальный пароль безопасным способом.'; await load() }
  catch (err) { error.value = (err as Error).message } finally { busy.value = false }
}
async function update(member: Member) {
  if (!window.confirm('Сохранить права сотрудника? Его текущие сессии будут завершены.')) { await load(); return }
  busy.value = true; error.value = ''; notice.value = ''
  try { await api('/team/' + member.id, { method: 'PATCH', body: JSON.stringify({ role: member.role, status: member.status }) }); if (member.id === session.user?.user_id) forgetSession(); else { notice.value = 'Права обновлены. Сессии сотрудника завершены.'; await load() } }
  catch (err) { error.value = (err as Error).message; await load() } finally { busy.value = false }
}
onMounted(load)
</script>
<template>
  <div class="page-heading"><div><p class="eyebrow">УПРАВЛЕНИЕ ДОСТУПОМ</p><h1>Ваша команда</h1><p class="subtitle">Сотрудники и их роли в организации.</p></div><Users :size="27" /></div>
  <p v-if="error" class="form-error" role="alert">{{ error }}</p><p v-if="notice" class="form-success" role="status">{{ notice }}</p>
  <section class="editor-card"><h2>Добавить сотрудника</h2><label class="form-field">Способ добавления<select v-model="byInvitation" :disabled="busy"><option :value="true">Приглашение по почте</option><option :value="false">Начальный пароль вручную</option></select></label><form class="team-create-form" @submit.prevent="create"><label class="form-field">Имя сотрудника<input v-model="form.display_name" required maxlength="250" /></label><label class="form-field">Почта сотрудника<input v-model="form.email" type="email" required maxlength="320" autocomplete="off" /></label><label v-if="!byInvitation" class="form-field">Начальный пароль<input v-model="form.password" type="password" minlength="12" maxlength="1024" autocomplete="new-password" required /><span class="field-hint">Минимум 12 символов.</span></label><label class="form-field">Роль сотрудника<select v-model="form.role"><option v-for="(name, role) in roleNames" :key="role" :value="role">{{ name }}</option></select></label><button class="primary-link" :disabled="busy"><Plus :size="17" />Добавить сотрудника</button></form></section>
  <section class="team-list" aria-label="Сотрудники"><article v-for="member in items" :key="member.id" class="team-row"><div class="member-name"><strong>{{ member.display_name }}</strong><span>{{ member.email }}</span></div><label class="form-field">Роль<select v-model="member.role" :aria-label="'Роль ' + member.display_name" :disabled="busy"><option v-for="(name, role) in roleNames" :key="role" :value="role">{{ name }}</option></select></label><label class="form-field">Доступ<select v-model="member.status" :aria-label="'Доступ ' + member.display_name" :disabled="busy"><option v-if="member.status === 'invited'" value="invited">Приглашён</option><option value="active">Активен</option><option value="disabled">Отключён</option></select></label><button class="secondary-button" :disabled="busy" @click="update(member)">Сохранить права</button><button v-if="member.status === 'invited'" class="text-button" :disabled="busy" @click="resend(member)">Отправить новое приглашение</button></article></section>
</template>
