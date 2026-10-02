<script setup>
import { ref, computed } from 'vue'
import Icon from '../ui/Icon.vue'
import Seg from '../ui/Seg.vue'
import { t } from '../i18n'
import { users, loadingUsers, used, fmtGB, fmtDate, isExpired, quotaPct, openUserModal, openShare, toggleUserStatus, deleteUser } from '../store'

const search = ref('')
const filter = ref('all')

const state = (u) => {
  if (u.Status !== 'active') return 'blocked'
  if (isExpired(u)) return 'expired'
  if ((quotaPct(u) ?? 0) >= 100) return 'limit'
  return 'active'
}
const stateClass = { active: 'bg-ok-bg text-ok', blocked: 'bg-surf2 text-mute', expired: 'bg-warn-bg text-warn', limit: 'bg-err-bg text-err' }
const nearLimit = (u) => (quotaPct(u) ?? 0) >= 90

const counts = computed(() => ({
  all: users.value.length,
  active: users.value.filter(u => state(u) === 'active').length,
  limit: users.value.filter(u => nearLimit(u) || isExpired(u)).length,
  blocked: users.value.filter(u => u.Status !== 'active').length,
}))
const filters = computed(() => ['all', 'active', 'limit', 'blocked'].map(v => ({ value: v, label: `${t('us.f.' + v)} · ${counts.value[v]}` })))

const list = computed(() => {
  const q = search.value.trim().toLowerCase()
  return users.value.filter(u => {
    if (q && !u.Name.toLowerCase().includes(q)) return false
    if (filter.value === 'active') return state(u) === 'active'
    if (filter.value === 'limit') return nearLimit(u) || isExpired(u)
    if (filter.value === 'blocked') return u.Status !== 'active'
    return true
  })
})
const maxUsed = computed(() => Math.max(1, ...users.value.map(used)))
const barPct = (u) => quotaPct(u) ?? Math.round((used(u) / maxUsed.value) * 100)
const barClass = (u) => ((quotaPct(u) ?? 0) >= 100 ? 'bg-err' : nearLimit(u) ? 'bg-warn' : 'bg-acc')
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-col md:flex-row gap-3 md:items-center">
      <label class="relative flex-1">
        <span class="sr-only">{{ t('us.search') }}</span>
        <Icon name="search" :size="16" class="absolute left-3 top-1/2 -translate-y-1/2 text-dim" />
        <input v-model="search" type="search" :placeholder="t('us.search')" class="input pl-9">
      </label>
      <div class="overflow-x-auto -mx-4 px-4 md:mx-0 md:px-0"><Seg v-model="filter" :options="filters" :label="t('us.filter')" class="!flex-nowrap w-max" /></div>
    </div>

    <div v-if="loadingUsers && !users.length" class="text-mute">{{ t('loading') }}</div>
    <p v-else-if="!list.length" class="card text-center text-mute py-10">{{ users.length ? t('us.nothing') : t('noUsers') }}</p>

    <!-- Компьютер: таблица -->
    <section v-if="list.length" class="hidden md:block bg-surf border border-line rounded-xl overflow-hidden">
      <table class="w-full text-sm">
        <thead>
          <tr class="text-left text-xs uppercase tracking-wide text-dim">
            <th class="font-medium px-4 py-3">{{ t('name') }}</th>
            <th class="font-medium px-4 py-3">{{ t('status') }}</th>
            <th class="font-medium px-4 py-3 w-[28%]">{{ t('us.traffic') }}</th>
            <th class="font-medium px-4 py-3">{{ t('us.until') }}</th>
            <th class="font-medium px-4 py-3">{{ t('us.devices') }}</th>
            <th class="px-4 py-3"><span class="sr-only">{{ t('actions') }}</span></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in list" :key="u.ID" class="border-t border-line hover:bg-surf2/40">
            <td class="px-4 py-3 font-medium">{{ u.Name }}</td>
            <td class="px-4 py-3"><span :class="['pill', stateClass[state(u)]]">{{ t('us.s.' + state(u)) }}</span></td>
            <td class="px-4 py-3">
              <div class="flex flex-col gap-1.5">
                <span class="text-[13px] tabular-nums">{{ fmtGB(used(u)) }} <span class="text-dim">/ {{ u.TrafficQuota ? fmtGB(u.TrafficQuota) + ' ' + t('gb') : t('us.noLimit') }}</span></span>
                <div class="h-1.5 rounded-full bg-surf2"><div :class="['h-1.5 rounded-full', barClass(u)]" :style="{ width: barPct(u) + '%' }"></div></div>
              </div>
            </td>
            <td :class="['px-4 py-3', isExpired(u) ? 'text-warn' : 'text-mute']">{{ u.ExpiresAt ? fmtDate(u.ExpiresAt) : t('us.noExpiry') }}</td>
            <td class="px-4 py-3 text-mute">{{ u.IPLimit || '∞' }}</td>
            <td class="px-4 py-3">
              <div class="flex gap-1.5 justify-end">
                <button type="button" class="icon-btn" :aria-label="t('shareLink')" :title="t('shareLink')" @click="openShare(u)"><Icon name="link" :size="16" /></button>
                <button type="button" class="icon-btn" :aria-label="u.Status === 'active' ? t('block') : t('unblock')" :title="u.Status === 'active' ? t('block') : t('unblock')" @click="toggleUserStatus(u)">
                  <Icon :name="u.Status === 'active' ? 'pause' : 'play'" :size="16" /></button>
                <button type="button" class="icon-btn" :aria-label="t('editUser')" :title="t('editUser')" @click="openUserModal(u)"><Icon name="edit" :size="16" /></button>
                <button type="button" class="icon-btn text-err hover:text-err" :aria-label="t('delete')" :title="t('delete')" @click="deleteUser(u)"><Icon name="trash" :size="16" /></button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- Телефон: карточки -->
    <div v-if="list.length" class="md:hidden flex flex-col gap-3">
      <section v-for="u in list" :key="u.ID" class="card gap-3">
        <div class="flex items-center justify-between gap-3">
          <span class="font-medium truncate">{{ u.Name }}</span>
          <span :class="['pill', stateClass[state(u)]]">{{ t('us.s.' + state(u)) }}</span>
        </div>
        <div class="flex flex-col gap-1.5">
          <div class="flex justify-between text-[13px]">
            <span class="tabular-nums">{{ fmtGB(used(u)) }} <span class="text-dim">/ {{ u.TrafficQuota ? fmtGB(u.TrafficQuota) + ' ' + t('gb') : t('us.noLimit') }}</span></span>
            <span :class="isExpired(u) ? 'text-warn' : 'text-dim'">{{ u.ExpiresAt ? t('us.untilDate', { d: fmtDate(u.ExpiresAt) }) : t('us.noExpiry') }}</span>
          </div>
          <div class="h-1.5 rounded-full bg-surf2"><div :class="['h-1.5 rounded-full', barClass(u)]" :style="{ width: barPct(u) + '%' }"></div></div>
        </div>
        <div class="flex gap-2">
          <button type="button" class="btn flex-1" @click="openShare(u)"><Icon name="link" :size="16" />{{ t('us.share') }}</button>
          <button type="button" class="icon-btn" :aria-label="u.Status === 'active' ? t('block') : t('unblock')" @click="toggleUserStatus(u)"><Icon :name="u.Status === 'active' ? 'pause' : 'play'" :size="16" /></button>
          <button type="button" class="icon-btn" :aria-label="t('editUser')" @click="openUserModal(u)"><Icon name="edit" :size="16" /></button>
          <button type="button" class="icon-btn text-err hover:text-err" :aria-label="t('delete')" @click="deleteUser(u)"><Icon name="trash" :size="16" /></button>
        </div>
      </section>
    </div>
  </div>
</template>
