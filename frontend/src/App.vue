<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { LayoutDashboard, Library, ClipboardList, Users, ChartNoAxesCombined, Settings, Menu, X, ArrowUpRight, Layers, LogOut, UserRoundCog, BriefcaseBusiness, Link } from '@lucide/vue'
import { computed } from 'vue'
import { session, logout, roleNames } from './session'
const menuOpen = ref(false)
const route = useRoute()
watch(() => route.path, () => { menuOpen.value = false })
const links = [
  { path: '/dashboard', label: 'Обзор', icon: LayoutDashboard },
  { path: '/questions', label: 'Банк вопросов', icon: Library },
  { path: '/tests', label: 'Тесты', icon: ClipboardList },
  { path: '/candidates', label: 'Кандидаты', icon: Users },
  { path: '/vacancies', label: 'Вакансии', icon: BriefcaseBusiness },
  { path: '/invitations', label: 'Приглашения', icon: Link },
  { path: '/results', label: 'Результаты', icon: ChartNoAxesCombined },
]
const visibleLinks = computed(() => links.filter(link => session.user?.role !== 'assessor' || ['/vacancies', '/candidates', '/results'].includes(link.path)))
const standalone = computed(() => route.path === '/login' || route.meta.public)
const logoutError = ref('')
async function signOut() { try { await logout() } catch (err) { logoutError.value = (err as Error).message } }
</script>

<template>
  <RouterView v-if="standalone" :key="$route.path" />
  <a v-else class="skip-link" href="#main">Перейти к содержимому</a>
  <div v-if="!standalone" class="app-shell">
    <div v-if="menuOpen" class="menu-overlay" @click="menuOpen = false"></div>
    <aside id="sidebar" class="sidebar" :class="{ open: menuOpen }" aria-label="Главное меню">
      <RouterLink to="/dashboard" class="brand"><span class="brand-icon"><Layers :size="23" /></span><span>Кадровый<span class="brand-second">тестер<span class="version">2.0</span></span></span></RouterLink>
      <button class="mobile-close icon-button" aria-label="Закрыть меню" @click="menuOpen = false"><X /></button>
      <div class="workspace-label">РАБОЧЕЕ ПРОСТРАНСТВО</div>
      <nav><RouterLink v-for="link in visibleLinks" :key="link.path" :to="link.path" class="nav-link"><component :is="link.icon" :size="20" /><span>{{ link.label }}</span></RouterLink><RouterLink v-if="session.user?.role === 'org_admin'" to="/team" class="nav-link"><UserRoundCog :size="20" />Команда</RouterLink></nav>
      <div class="sidebar-bottom">
        <div v-if="session.user?.role !== 'assessor'" class="sidebar-note"><span class="note-icon">✦</span><strong>От вопроса к решению</strong><p>Тесты, которые помогают лучше узнать кандидата.</p><RouterLink to="/tests">К тестам <ArrowUpRight :size="16" /></RouterLink></div>
        <RouterLink to="/settings" class="nav-link"><Settings :size="20" />Настройки</RouterLink>
        <div class="workspace-identity"><span class="avatar">{{ session.user?.display_name.slice(0, 2).toUpperCase() }}</span><div><strong>{{ session.user?.display_name }}</strong><span>{{ session.user && roleNames[session.user.role] }}</span></div><button class="icon-button" aria-label="Выйти" @click="signOut"><LogOut :size="17" /></button></div><p v-if="logoutError" class="form-error" role="alert">{{ logoutError }}</p>
      </div>
    </aside>
    <div class="main-shell">
      <header class="topbar"><div class="topbar-left"><button class="mobile-toggle icon-button" aria-label="Открыть меню" :aria-expanded="menuOpen" aria-controls="sidebar" @click="menuOpen = !menuOpen"><Menu /></button><span>Рабочее пространство</span><span class="topbar-slash">/</span><strong>{{ session.user?.organization_name }}</strong></div><span class="stage-badge"><span></span>Версия в разработке</span></header>
      <main id="main" tabindex="-1"><RouterView /></main>
      <footer><span>Кадровый тестер © 2026</span><span>Создаём основу для точной оценки</span></footer>
    </div>
  </div>
</template>
