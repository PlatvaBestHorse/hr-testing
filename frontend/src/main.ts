import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import Workspace from './Workspace.vue'
import Login from './Login.vue'
import Questions from './Questions.vue'
import LegacyImport from './LegacyImport.vue'
import QuestionEditor from './QuestionEditor.vue'
import Team from './Team.vue'
import Tests from './Tests.vue'
import TestBuilder from './TestBuilder.vue'
import Directory from './Directory.vue'
import Invitations from './Invitations.vue'
import InviteLanding from './InviteLanding.vue'
import Results from './Results.vue'
import ResultReport from './ResultReport.vue'
import Audit from './Audit.vue'
import Retention from './Retention.vue'
import AccountSecurity from './AccountSecurity.vue'
import PasswordRecovery from './PasswordRecovery.vue'
import OrganizationSettings from './OrganizationSettings.vue'
import { loadSession, session } from './session'
import './style.css'
import './workflow.css'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/login', component: Login },
    { path: '/forgot-password', component: PasswordRecovery, meta: { public: true } },
    { path: '/reset-password', component: PasswordRecovery, meta: { public: true } },
    { path: '/security', component: AccountSecurity },
    { path: '/settings', component: OrganizationSettings },
    { path: '/questions', component: Questions },
    { path: '/questions/new', component: QuestionEditor },
    { path: '/questions/import', component: LegacyImport },
    { path: '/questions/:id', component: QuestionEditor },
    { path: '/team', component: Team },
    { path: '/tests', component: Tests },
    { path: '/tests/new', component: TestBuilder },
    { path: '/tests/:id/edit', component: TestBuilder },
    ...(['candidates', 'vacancies'] as const).flatMap(kind => ['', '/new', '/:id'].map(suffix => ({ path: '/' + kind + suffix, component: Directory, props: { kind } }))),
    { path: '/invitations', component: Invitations },
    { path: '/i/:token', component: InviteLanding, meta: { public: true } },
    { path: '/results', component: Results },
    { path: '/results/:id', component: ResultReport },
    { path: '/audit', component: Audit },
    { path: '/retention', component: Retention },
    ...['dashboard'].map(page => ({
      path: `/${page}`, component: Workspace, props: { page },
    })),
    { path: '/:pathMatch(.*)*', component: Workspace, props: { page: 'not-found' } },
  ],
})
router.beforeEach(async to => {
  if (to.meta.public) return true
  try { await loadSession() } catch { return to.path === '/login' ? true : '/login' }
  if (to.path === '/login') return session.user ? '/dashboard' : true
  if (!session.user) return '/login'
  if (['/team', '/audit', '/retention', '/questions/import'].includes(to.path) && session.user.role !== 'org_admin') return session.user.role === 'assessor' ? '/results' : '/dashboard'
  if (session.user.role === 'assessor' && ['/dashboard', '/questions', '/tests', '/invitations', '/candidates/new', '/vacancies/new'].some(path => to.path === path || to.path.startsWith(path + '/'))) return '/results'
  return true
})
window.addEventListener('hr-session-expired', () => { router.replace('/login') })
createApp(App).use(router).mount('#app')
