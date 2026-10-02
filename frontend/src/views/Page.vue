<script setup>
import Icon from '../ui/Icon.vue'
import SaveBar from '../ui/SaveBar.vue'
import { t } from '../i18n'
import { page, pageDefaults, platforms, savePage } from '../store'

const addClient = () => page.value.clients.push({ name: '', deeplink: '', downloads: {} })
const removeClient = (i) => page.value.clients.splice(i, 1)
const moveClient = (i, d) => {
  const list = page.value.clients
  const j = i + d
  if (j < 0 || j >= list.length) return
  ;[list[i], list[j]] = [list[j], list[i]]
}
const resetClients = () => {
  if (!confirm(t('confirmResetClients'))) return
  page.value.clients = JSON.parse(JSON.stringify(pageDefaults.value.clients))
}
</script>

<template>
  <div v-if="!page" class="text-mute">{{ t('loading') }}</div>
  <div v-else class="flex flex-col gap-4 sm:gap-6 max-w-3xl">
    <section class="card">
      <div class="grid sm:grid-cols-2 gap-4">
        <div>
          <label class="label" for="pg-title">{{ t('profileTitle') }}</label>
          <input id="pg-title" v-model="page.title" type="text" maxlength="32" class="input">
          <p class="hint mt-1.5">{{ t('profileTitleHint') }}</p>
        </div>
        <div>
          <label class="label" for="pg-support">{{ t('supportURL') }}</label>
          <input id="pg-support" v-model="page.support_url" type="text" placeholder="https://t.me/username" class="input">
          <p class="hint mt-1.5">{{ t('supportURLHint') }}</p>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="flex flex-wrap justify-between items-center gap-2">
        <h2 class="h2">{{ t('apps') }}</h2>
        <div class="flex gap-2">
          <button type="button" class="btn h-9" @click="resetClients">{{ t('resetDefaults') }}</button>
          <button type="button" class="btn h-9" @click="addClient"><Icon name="plus" :size="16" />{{ t('pg.addApp') }}</button>
        </div>
      </div>
      <p class="hint -mt-2">{{ t('appsHint', { url: '{url}', url_enc: '{url_enc}', name: '{name}' }) }}</p>
      <div v-for="(app, i) in page.clients" :key="i" class="border border-line rounded-lg p-3 sm:p-4 flex flex-col gap-3">
        <div class="flex gap-2 items-center">
          <input v-model="app.name" type="text" :placeholder="t('appName')" :aria-label="t('appName')" class="input flex-1 min-w-0">
          <button type="button" class="icon-btn" :aria-label="t('moveUp')" @click="moveClient(i, -1)"><Icon name="up" :size="16" /></button>
          <button type="button" class="icon-btn" :aria-label="t('moveDown')" @click="moveClient(i, 1)"><Icon name="chevron" :size="16" /></button>
          <button type="button" class="icon-btn text-err hover:text-err" :aria-label="t('delete')" @click="removeClient(i)"><Icon name="trash" :size="16" /></button>
        </div>
        <div>
          <label class="block text-xs text-dim mb-1">{{ t('deeplink') }}</label>
          <input v-model="app.deeplink" type="text" placeholder="app://import/{url}" class="input font-mono text-sm">
        </div>
        <div class="grid sm:grid-cols-2 gap-2">
          <div v-for="pl in platforms" :key="pl">
            <label class="block text-xs text-dim mb-1">{{ t('platforms.' + pl) }}</label>
            <input v-model="app.downloads[pl]" type="text" placeholder="https://…" class="input text-sm">
          </div>
        </div>
      </div>
    </section>

    <SaveBar @save="savePage" />
  </div>
</template>
