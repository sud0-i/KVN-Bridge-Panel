<script setup>
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { t, lang, setLang } from './i18n'
import Icon from './ui/Icon.vue'
import Sheet from './ui/Sheet.vue'
import Overview from './views/Overview.vue'
import Users from './views/Users.vue'
import Nodes from './views/Nodes.vue'
import Protocols from './views/Protocols.vue'
import Routing from './views/Routing.vue'
import Page from './views/Page.vue'
import Settings from './views/Settings.vue'
import {
  token, login, NeedCode, loadAll, logout, users, nodes, agentSHA, attention, saveMessage, saveError,
  userModal, openUserModal, saveUser, userAction, importUsers, fmtGB, used,
  shareUser, shareQR, subLink, copySubLink, nodeModal, openDeploy, deployNode, downloadBackup
} from './store'

// --- ВХОД ---
const loginPassword = ref('')
const loginCode = ref('')
const needCode = ref(false)
const loginError = ref('')
const isLoggingIn = ref(false)
const codeInput = ref(null)
const doLogin = async () => {
  isLoggingIn.value = true
  loginError.value = ''
  try {
    await login(loginPassword.value, loginCode.value)
    loginPassword.value = ''
    loginCode.value = ''
    needCode.value = false
  } catch (e) {
    if (e instanceof NeedCode) {
      needCode.value = true
      loginCode.value = ''
      setTimeout(() => codeInput.value?.focus(), 0)
    }
    loginError.value = e.message
  } finally {
    isLoggingIn.value = false
  }
}

// --- РАЗДЕЛЫ (адрес в #, чтобы обновление страницы оставляло на месте) ---
const views = {
  overview: { icon: 'overview', comp: Overview },
  users: { icon: 'users', comp: Users },
  nodes: { icon: 'nodes', comp: Nodes },
  protocols: { icon: 'protocols', comp: Protocols },
  routing: { icon: 'routing', comp: Routing },
  page: { icon: 'page', comp: Page },
  settings: { icon: 'settings', comp: Settings },
}
const mainTabs = ['overview', 'users', 'nodes', 'protocols']
const moreTabs = ['routing', 'page', 'settings']

const fromHash = () => {
  const v = location.hash.replace(/^#\/?/, '')
  return views[v] ? v : 'overview'
}
const view = ref(fromHash())
const moreOpen = ref(false)
const go = (v) => {
  moreOpen.value = false
  if (view.value === v) return
  view.value = v
  history.replaceState(null, '', '#/' + v)
  saveMessage.value = ''
  saveError.value = ''
  window.scrollTo(0, 0)
}
const onHash = () => { view.value = fromHash() }
onMounted(() => window.addEventListener('hashchange', onHash))
onBeforeUnmount(() => window.removeEventListener('hashchange', onHash))

// Счётчики в меню: проблемы нод и пользователи, требующие внимания
const badges = computed(() => ({
  nodes: attention.value.filter(a => a.view === 'nodes' && a.kind !== 'acc').length,
  users: attention.value.filter(a => a.view === 'users').length,
}))
const moreBadge = computed(() => moreTabs.some(v => badges.value[v]))

// Главное действие раздела — кнопка в шапке
const primary = computed(() => ({
  overview: { label: t('nav.addUser'), run: () => openUserModal() },
  users: { label: t('nav.addUser'), run: () => openUserModal() },
  nodes: { label: t('nav.addNode'), run: openDeploy },
}[view.value]))

// --- ОБНОВЛЕНИЕ ДАННЫХ: раз в 30 секунд, пока вкладка открыта ---
let timer
const tick = () => { if (token.value && !document.hidden) { loadAll() } }
onMounted(() => {
  if (token.value) loadAll()
  timer = setInterval(tick, 30000)
})
onBeforeUnmount(() => clearInterval(timer))
watch(token, (v) => { if (!v) view.value = 'overview' })

// --- ИМПОРТ ---
const importOpen = ref(false)
const importText = ref('')
const importError = ref('')
const importProblems = ref([])
const importing = ref(false)
const openImport = () => { importOpen.value = true; importError.value = ''; importProblems.value = [] }
const doImport = async () => {
  importing.value = true
  const err = await importUsers(importText.value)
  importing.value = false
  if (err) {
    importError.value = err.error
    importProblems.value = err.problems
    return
  }
  importText.value = ''
  importOpen.value = false
}

const savingUser = ref(false)
const doSaveUser = async () => {
  savingUser.value = true
  try { await saveUser() } finally { savingUser.value = false }
}
// Закрытие окон: в шаблоне нельзя присваивать импортированным ref
const closeUser = () => { userModal.value = null }
const closeShare = () => { shareUser.value = null }
const closeNode = () => { nodeModal.value = null }
const host = location.host

const deploying = ref(false)
const doDeploy = async () => {
  deploying.value = true
  try { await deployNode() } finally { deploying.value = false }
}
</script>

<template>
  <!-- ВХОД -->
  <div v-if="!token" class="min-h-[100dvh] flex items-center justify-center p-4 relative">
    <div class="absolute top-3 right-3 flex gap-1 text-xs">
      <button v-for="l in ['ru', 'en']" :key="l" type="button" @click="setLang(l)"
        :class="['h-8 px-2.5 rounded-md border transition-colors', lang === l ? 'border-acc text-acc' : 'border-line text-dim hover:text-fg']">{{ l.toUpperCase() }}</button>
    </div>
    <div class="bg-surf border border-line rounded-2xl p-8 w-full max-w-sm flex flex-col gap-6">
      <div class="flex flex-col items-center gap-3 text-center">
        <div class="w-11 h-11 rounded-xl bg-acc text-[#0B1220] flex items-center justify-center font-bold text-xl">K</div>
        <h1 class="text-2xl font-semibold">KVN</h1>
        <p class="text-sm text-mute">{{ t('loginHint') }}</p>
      </div>
      <form @submit.prevent="doLogin" class="flex flex-col gap-4">
        <label class="sr-only" for="password">{{ t('loginHint') }}</label>
        <input id="password" v-model="loginPassword" type="password" placeholder="••••••••" autocomplete="current-password" required
          :readonly="needCode" class="input h-12 text-center text-lg tracking-widest">
        <template v-if="needCode">
          <label class="text-sm text-mute text-center" for="code">{{ t('tf.loginCode') }}</label>
          <input id="code" ref="codeInput" v-model.trim="loginCode" inputmode="numeric" autocomplete="one-time-code" maxlength="12" required
            class="input h-12 text-center text-lg tracking-[0.3em] font-mono" placeholder="000000">
          <p class="hint text-center">{{ t('tf.loginRecovery') }}</p>
        </template>
        <p v-if="loginError" class="text-err text-sm text-center">{{ loginError }}</p>
        <button type="submit" :disabled="isLoggingIn || !loginPassword || (needCode && !loginCode)" class="btn btn-primary h-12">{{ isLoggingIn ? t('checking') : t('login') }}</button>
      </form>
    </div>
  </div>

  <!-- ПАНЕЛЬ -->
  <div v-else class="min-h-[100dvh] lg:flex">
    <!-- Боковое меню (компьютер) -->
    <nav :aria-label="t('nav.sections')" class="hidden lg:flex w-60 shrink-0 bg-side border-r border-line flex-col gap-1 px-3.5 py-5 sticky top-0 h-[100dvh]">
      <div class="flex items-center gap-2.5 px-2.5 pb-5">
        <div class="w-[30px] h-[30px] rounded-lg bg-acc text-[#0B1220] flex items-center justify-center font-bold">K</div>
        <div class="flex flex-col min-w-0"><span class="font-bold text-[15px]">KVN</span><span class="text-xs text-dim font-mono truncate">{{ host }}</span></div>
      </div>
      <a v-for="(v, key) in views" :key="key" :href="'#/' + key" @click.prevent="go(key)" :aria-current="view === key ? 'page' : undefined"
        :class="['flex items-center gap-3 h-10 px-3 rounded-lg text-sm transition-colors', view === key ? 'bg-surf2 text-fg font-semibold' : 'text-mute hover:text-fg hover:bg-surf2/50']">
        <Icon :name="v.icon" :class="view === key ? 'text-acc' : 'text-dim'" />
        <span class="flex-1">{{ t('nav.' + key) }}</span>
        <span v-if="badges[key]" class="min-w-5 h-5 px-1.5 rounded-full bg-warn-bg text-warn text-xs font-semibold flex items-center justify-center">{{ badges[key] }}</span>
      </a>
      <div class="mt-auto border-t border-line pt-3.5 px-2.5 flex flex-col gap-2.5">
        <span v-if="agentSHA" class="text-xs text-dim font-mono">{{ t('nav.master') }} {{ agentSHA.slice(0, 7) }}</span>
        <button type="button" class="flex items-center gap-2.5 text-sm text-mute hover:text-fg" @click="logout"><Icon name="logout" :size="16" class="text-dim" />{{ t('logout') }}</button>
      </div>
    </nav>

    <div class="flex-1 min-w-0 flex flex-col">
      <!-- Верхняя панель (телефон) -->
      <header class="lg:hidden sticky top-0 z-20 h-14 flex items-center gap-3 px-4 bg-side/95 backdrop-blur border-b border-line pt-[env(safe-area-inset-top)] box-content">
        <div class="w-7 h-7 rounded-lg bg-acc text-[#0B1220] flex items-center justify-center font-bold text-sm">K</div>
        <h1 class="font-semibold text-base flex-1 truncate">{{ t('nav.' + view) }}</h1>
        <button v-if="view === 'users'" type="button" class="icon-btn" :aria-label="t('importUsers')" @click="openImport"><Icon name="upload" :size="18" /></button>
        <button v-if="primary" type="button" class="w-11 h-11 rounded-xl bg-acc text-[#0B1220] flex items-center justify-center" :aria-label="primary.label" @click="primary.run()"><Icon name="plus" :size="20" /></button>
      </header>

      <main class="flex-1 px-4 sm:px-6 lg:px-10 pt-4 sm:pt-6 lg:pt-8 pb-[calc(88px+env(safe-area-inset-bottom))] lg:pb-10 flex flex-col gap-4 sm:gap-6 w-full max-w-[1400px]">
        <!-- Заголовок раздела (компьютер) -->
        <header class="hidden lg:flex items-end justify-between gap-6">
          <div class="flex flex-col gap-1.5">
            <h1 class="text-[26px] font-semibold tracking-tight">{{ t('nav.' + view) }}</h1>
            <p class="text-sm text-mute">{{ t('sub.' + view) }}</p>
          </div>
          <div class="flex gap-2.5">
            <button v-if="view === 'overview'" type="button" class="btn" @click="downloadBackup"><Icon name="download" :size="16" />{{ t('backup') }}</button>
            <button v-if="view === 'users'" type="button" class="btn" @click="openImport"><Icon name="upload" :size="16" />{{ t('importUsers') }}</button>
            <button v-if="primary" type="button" class="btn btn-primary" @click="primary.run()"><Icon name="plus" :size="16" />{{ primary.label }}</button>
          </div>
        </header>
        <p class="lg:hidden text-[13px] text-mute -mt-1">{{ t('sub.' + view) }}</p>

        <component :is="views[view].comp" @go="go" />
      </main>
    </div>

    <!-- Нижние вкладки (телефон) -->
    <nav :aria-label="t('nav.sections')" class="lg:hidden fixed bottom-0 inset-x-0 z-30 bg-side/95 backdrop-blur border-t border-line flex pb-[env(safe-area-inset-bottom)]">
      <a v-for="key in mainTabs" :key="key" :href="'#/' + key" @click.prevent="go(key)" :aria-current="view === key ? 'page' : undefined"
        :class="['flex-1 h-16 flex flex-col items-center justify-center gap-1 text-[11px] relative', view === key ? 'text-acc' : 'text-dim']">
        <Icon :name="views[key].icon" :size="22" />
        <span>{{ t('tab.' + key) }}</span>
        <span v-if="badges[key]" class="absolute top-2 left-1/2 ml-2 w-2 h-2 rounded-full bg-warn"></span>
      </a>
      <button type="button" @click="moreOpen = true" :aria-expanded="moreOpen"
        :class="['flex-1 h-16 flex flex-col items-center justify-center gap-1 text-[11px] relative', moreTabs.includes(view) ? 'text-acc' : 'text-dim']">
        <Icon name="more" :size="22" />
        <span>{{ t('tab.more') }}</span>
        <span v-if="moreBadge" class="absolute top-2 left-1/2 ml-2 w-2 h-2 rounded-full bg-warn"></span>
      </button>
    </nav>

    <!-- «Ещё» (телефон) -->
    <Sheet v-if="moreOpen" :title="t('tab.more')" @close="moreOpen = false">
      <div class="flex flex-col -mx-2">
        <a v-for="key in moreTabs" :key="key" :href="'#/' + key" @click.prevent="go(key)"
          :class="['flex items-center gap-3 h-12 px-3 rounded-lg', view === key ? 'bg-surf2 text-fg' : 'text-mute']">
          <Icon :name="views[key].icon" :class="view === key ? 'text-acc' : 'text-dim'" />
          <span class="flex-1 text-[15px]">{{ t('nav.' + key) }}</span>
        </a>
        <button type="button" class="flex items-center gap-3 h-12 px-3 rounded-lg text-mute" @click="downloadBackup(); moreOpen = false">
          <Icon name="download" class="text-dim" /><span class="text-[15px]">{{ t('backup') }}</span></button>
        <button type="button" class="flex items-center gap-3 h-12 px-3 rounded-lg text-err" @click="logout">
          <Icon name="logout" /><span class="text-[15px]">{{ t('logout') }}</span></button>
      </div>
    </Sheet>

    <!-- Пользователь -->
    <Sheet v-if="userModal" :title="userModal.editing ? t('editUserTitle', { name: userModal.editing.Name }) : t('newUser')" @close="closeUser">
      <div>
        <label class="label" for="u-name">{{ t('userNameHint') }}</label>
        <input id="u-name" v-model="userModal.form.name" type="text" class="input" placeholder="user_123" autocapitalize="off" autocomplete="off">
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="label" for="u-ip">{{ t('ipLimitHint') }}</label>
          <input id="u-ip" v-model="userModal.form.ipLimit" type="number" inputmode="numeric" min="0" class="input">
        </div>
        <div>
          <label class="label" for="u-quota">{{ t('quotaHint') }}</label>
          <input id="u-quota" v-model="userModal.form.quotaGB" type="number" inputmode="decimal" min="0" step="any" class="input">
        </div>
      </div>
      <div>
        <label class="label" for="u-exp">{{ t('expiresHint') }}</label>
        <input id="u-exp" v-model="userModal.form.expires" type="date" class="input">
      </div>
      <div v-if="userModal.editing" class="border-t border-line pt-4 flex flex-col gap-2.5">
        <p class="text-xs text-dim">{{ t('trafficUsed') }}: {{ fmtGB(used(userModal.editing)) }} {{ t('gb') }}</p>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn h-9" @click="userAction('reset-traffic', null, 'confirmResetTraffic')">{{ t('resetTraffic') }}</button>
          <button type="button" class="btn h-9" @click="userAction('rotate', { what: 'link' }, 'confirmNewLink')">{{ t('newLink') }}</button>
          <button type="button" class="btn btn-danger h-9" @click="userAction('rotate', { what: 'key' }, 'confirmNewKey')">{{ t('newKey') }}</button>
        </div>
      </div>
      <template #footer>
        <button type="button" class="btn" @click="closeUser">{{ t('cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="savingUser || !userModal.form.name" @click="doSaveUser">{{ t('save') }}</button>
      </template>
    </Sheet>

    <!-- Импорт -->
    <Sheet v-if="importOpen" :title="t('importTitle')" wide @close="importOpen = false">
      <p class="text-sm text-mute leading-relaxed">{{ t('importHint') }}</p>
      <textarea v-model="importText" rows="9" class="textarea" :aria-label="t('importTitle')"
        placeholder="alice 57d2bfa5b82efcc2d127dd64f5dce8c7&#10;bob https://old.example.com/sub/0a1b2c3d4e5f60718293a4b5c6d7e8f9 50 2026-12-31"></textarea>
      <div v-if="importError" class="text-sm text-err">
        <p class="font-medium">{{ importError }}</p>
        <ul class="list-disc ml-5"><li v-for="p in importProblems" :key="p">{{ p }}</li></ul>
      </div>
      <template #footer>
        <button type="button" class="btn" @click="importOpen = false">{{ t('cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="importing || !importText.trim()" @click="doImport">{{ t('import') }}</button>
      </template>
    </Sheet>

    <!-- Поделиться -->
    <Sheet v-if="shareUser" :title="shareUser.Name" @close="closeShare">
      <img v-if="shareQR" :src="shareQR" :alt="t('us.qr')" class="mx-auto rounded-lg bg-white w-64 h-64">
      <p class="text-xs text-mute break-all font-mono text-center">{{ subLink(shareUser) }}</p>
      <p class="hint text-center">{{ t('shareHint') }}</p>
      <template #footer>
        <button type="button" class="btn" @click="closeShare">{{ t('close') }}</button>
        <button type="button" class="btn btn-primary" @click="copySubLink(shareUser)"><Icon name="copy" :size="16" />{{ t('copyLink') }}</button>
      </template>
    </Sheet>

    <!-- Установка ноды -->
    <Sheet v-if="nodeModal" :title="nodeModal.redeployIP ? t('redeployTitle', { ip: nodeModal.redeployIP }) : t('autoDeploy')" @close="closeNode">
      <div v-if="nodeModal.message" class="bg-acc-bg border border-acc/30 text-acc p-4 rounded-lg text-sm">{{ nodeModal.message }}</div>
      <template v-else>
        <p v-if="nodeModal.redeployIP" class="text-sm text-mute leading-relaxed">{{ t('redeployHint') }}</p>
        <template v-else>
          <div>
            <label class="label" for="n-ip">{{ t('ip') }}</label>
            <input id="n-ip" v-model.trim="nodeModal.ip" type="text" inputmode="decimal" class="input font-mono" autocomplete="off">
          </div>
          <div>
            <label class="label" for="n-role">{{ t('role') }}</label>
            <select id="n-role" v-model="nodeModal.type" class="input">
              <option value="bridge">{{ t('bridgeOption') }}</option>
              <option value="exit">{{ t('exitOption') }}</option>
            </select>
          </div>
          <div>
            <label class="label" for="n-sni">{{ t('sniLabel') }}</label>
            <input id="n-sni" v-model.trim="nodeModal.sni" type="text" :placeholder="t('sniPlaceholder')" class="input" autocapitalize="off">
            <p class="hint mt-1.5">{{ t('sniHint') }}</p>
          </div>
        </template>
        <div>
          <label class="label" for="n-pass">{{ t('rootPassword') }}</label>
          <input id="n-pass" v-model="nodeModal.password" type="password" class="input" autocomplete="off">
          <p v-if="nodeModal.redeployIP && nodeModal.byKey" class="hint mt-1.5">{{ t('tm.redeployKey') }}</p>
        </div>
      </template>
      <template v-if="!nodeModal.message" #footer>
        <button type="button" class="btn" @click="closeNode">{{ t('cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="deploying || (!nodeModal.ip && !nodeModal.redeployIP) || (!nodeModal.password && !nodeModal.byKey)" @click="doDeploy">{{ t('startDeploy') }}</button>
      </template>
    </Sheet>
  </div>
</template>
