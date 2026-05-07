<script setup>
import { ref, onMounted } from 'vue'

// --- ГЛОБАЛЬНОЕ СОСТОЯНИЕ ---
const currentTab = ref('users') // 'users' или 'nodes'

// --- СОСТОЯНИЕ ПОЛЬЗОВАТЕЛЕЙ ---
const users = ref([])
const loadingUsers = ref(true)
const showUserModal = ref(false)
const newUserName = ref('')
const newUserLimit = ref(5)
const isSubmittingUser = ref(false)

// --- СОСТОЯНИЕ НОД (СЕРВЕРОВ) ---
const nodes = ref([])
const loadingNodes = ref(true)
const showNodeModal = ref(false)
const newNodeIP = ref('')
const newNodeType = ref('ru_bridge')
const newNodePassword = ref('')
const isDeploying = ref(false)
const deployMessage = ref('') // Сообщение об успешном запуске Ansible

// ================= ФУНКЦИИ ПОЛЬЗОВАТЕЛЕЙ =================
const fetchUsers = async () => {
  loadingUsers.value = true
  try {
    const response = await fetch('/api/users')
    users.value = await response.json()
  } catch (error) {
    console.error('Ошибка загрузки юзеров:', error)
  } finally {
    loadingUsers.value = false
  }
}

const addUser = async () => {
  if (!newUserName.value) return
  isSubmittingUser.value = true
  try {
    await fetch('/api/users', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: newUserName.value, ip_limit: newUserLimit.value })
    })
    showUserModal.value = false
    newUserName.value = ''
    newUserLimit.value = 5
    await fetchUsers()
  } catch (error) {
    console.error('Ошибка создания юзера:', error)
  } finally {
    isSubmittingUser.value = false
  }
}

const toggleUserStatus = async (user) => {
  const newStatus = user.Status === 'active' ? 'blocked' : 'active'
  await fetch(`/api/users/${user.ID}/status`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status: newStatus })
  })
  fetchUsers()
}

const deleteUser = async (id) => {
  if (!confirm('Удалить пользователя безвозвратно?')) return
  await fetch(`/api/users/${id}`, { method: 'DELETE' })
  fetchUsers()
}

// ================= ФУНКЦИИ НОД =================
const fetchNodes = async () => {
  loadingNodes.value = true
  try {
    const response = await fetch('/api/nodes')
    nodes.value = await response.json()
  } catch (error) {
    console.error('Ошибка загрузки нод:', error)
  } finally {
    loadingNodes.value = false
  }
}

const deployNode = async () => {
  if (!newNodeIP.value || !newNodePassword.value) return
  isDeploying.value = true
  deployMessage.value = ''
  
  try {
    const response = await fetch('/api/nodes', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ 
        ip: newNodeIP.value, 
        type: newNodeType.value,
        password: newNodePassword.value
      })
    })
    
    const data = await response.json()
    deployMessage.value = data.message // Показываем сообщение от Мастера
    
    // Очищаем форму, но закрываем модалку с задержкой, чтобы юзер прочитал текст
    newNodeIP.value = ''
    newNodePassword.value = ''
    
    setTimeout(() => {
      showNodeModal.value = false
      deployMessage.value = ''
      fetchNodes() // Обновляем список (нода появится со статусом Офлайн)
    }, 3000)

  } catch (error) {
    console.error('Ошибка запуска деплоя:', error)
  } finally {
    isDeploying.value = false
  }
}

// Загружаем данные при старте
onMounted(() => {
  fetchUsers()
  fetchNodes()
})
</script>

<template>
  <div class="min-h-screen p-8 relative">
    <div class="max-w-5xl mx-auto">
      
      <div class="flex justify-between items-end mb-8 border-b border-gray-700 pb-4">
        <h1 class="text-3xl font-bold text-blue-400">🚀 VPN Cluster</h1>
        <div class="flex gap-2">
          <button @click="currentTab = 'users'" 
            :class="['px-4 py-2 rounded-t-lg font-medium transition-colors border-b-2', currentTab === 'users' ? 'border-blue-500 text-blue-400 bg-gray-800' : 'border-transparent text-gray-400 hover:text-gray-200']">
            👥 Пользователи
          </button>
          <button @click="currentTab = 'nodes'" 
            :class="['px-4 py-2 rounded-t-lg font-medium transition-colors border-b-2', currentTab === 'nodes' ? 'border-blue-500 text-blue-400 bg-gray-800' : 'border-transparent text-gray-400 hover:text-gray-200']">
            🖥 Серверы (Узлы)
          </button>
        </div>
      </div>
      
      <div v-if="currentTab === 'users'" class="bg-gray-800 rounded-xl shadow-lg p-6 border border-gray-700 animate-fade-in">
        <div class="flex justify-between items-center mb-6">
          <h2 class="text-xl font-semibold">Список клиентов</h2>
          <button @click="showUserModal = true" class="bg-blue-600 hover:bg-blue-500 text-white px-4 py-2 rounded-lg font-medium transition-colors shadow-lg shadow-blue-900/20">
            + Добавить юзера
          </button>
        </div>

        <div v-if="loadingUsers" class="text-gray-400">Загрузка...</div>
        
        <table v-else class="w-full text-left border-collapse">
          <thead>
            <tr class="border-b border-gray-700 text-gray-400">
              <th class="py-3 font-medium">Имя</th>
              <th class="py-3 font-medium">Лимит IP</th>
              <th class="py-3 font-medium">Трафик</th>
              <th class="py-3 font-medium">Статус</th>
              <th class="py-3 font-medium">Действия</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="users.length === 0">
              <td colspan="4" class="py-8 text-center text-gray-500">Нет пользователей. Создайте первого!</td>
            </tr>
            <tr v-for="user in users" :key="user.ID" class="border-b border-gray-700/50 hover:bg-gray-700/20 transition-colors">
  <td class="py-3 font-medium">{{ user.Name }}</td>
  <td class="py-3">{{ user.IPLimit === 0 ? 'Безлимит' : user.IPLimit }}</td>
  <td class="py-3 text-sm text-gray-400">
    <span class="text-green-400">↓{{ (user.TrafficDown / 1073741824).toFixed(2) }} GB</span> / 
    <span class="text-blue-400">↑{{ (user.TrafficUp / 1073741824).toFixed(2) }} GB</span>
  </td>
  <td class="py-3">
    <span :class="['px-2 py-1 rounded-full text-xs font-medium', 
      user.Status === 'active' ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400']">
      {{ user.Status === 'active' ? 'Активен' : 'Заблокирован' }}
    </span>
  </td>

  <td class="py-3">
    <div class="flex gap-3">
      <button @click="toggleUserStatus(user)" 
        :title="user.Status === 'active' ? 'Заблокировать' : 'Разблокировать'"
        class="text-gray-400 hover:text-orange-400 transition-colors">
        <span v-if="user.Status === 'active'">⏸️</span>
        <span v-else>▶️</span>
      </button>

      <button @click="copySubLink(user.ID)" title="Скопировать ссылку" class="text-gray-400 hover:text-blue-400 transition-colors">
        🔗
      </button>

      <button @click="deleteUser(user.ID)" title="Удалить" class="text-gray-400 hover:text-red-500 transition-colors">
        🗑️
      </button>
    </div>
  </td>
</tr>
          </tbody>
        </table>
      </div>

      <div v-if="currentTab === 'nodes'" class="bg-gray-800 rounded-xl shadow-lg p-6 border border-gray-700 animate-fade-in">
        <div class="flex justify-between items-center mb-6">
          <h2 class="text-xl font-semibold">Инфраструктура кластера</h2>
          <button @click="showNodeModal = true" class="bg-indigo-600 hover:bg-indigo-500 text-white px-4 py-2 rounded-lg font-medium transition-colors shadow-lg shadow-indigo-900/20">
            + Развернуть сервер
          </button>
        </div>

        <div v-if="loadingNodes" class="text-gray-400">Загрузка...</div>
        
        <table v-else class="w-full text-left border-collapse">
          <thead>
            <tr class="border-b border-gray-700 text-gray-400">
              <th class="py-3 font-medium">IP Адрес</th>
              <th class="py-3 font-medium">Роль</th>
              <th class="py-3 font-medium">Домен/SNI</th>
              <th class="py-3 font-medium">Статус</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="nodes.length === 0">
              <td colspan="4" class="py-8 text-center text-gray-500">Узлы не найдены. Разверните первый сервер!</td>
            </tr>
            <tr v-for="node in nodes" :key="node.IP" class="border-b border-gray-700/50 hover:bg-gray-700/20 transition-colors">
              <td class="py-3 font-mono font-medium text-gray-200">{{ node.IP }}</td>
              <td class="py-3">
                <span v-if="node.Type === 'ru_bridge'" class="text-orange-400">🇷🇺 RU Мост</span>
                <span v-else class="text-blue-400">🇪🇺 EU Экзит</span>
              </td>
              <td class="py-3 text-sm text-gray-400">{{ node.Domain || node.SNI || '—' }}</td>
              <td class="py-3">
                <span v-if="node.IsOnline" class="bg-green-500/20 text-green-400 px-2 py-1 rounded-full text-xs font-medium">Онлайн</span>
                <span v-else class="bg-yellow-500/20 text-yellow-400 px-2 py-1 rounded-full text-xs font-medium animate-pulse">Установка / Офлайн</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-if="showUserModal" class="fixed inset-0 bg-black/60 flex items-center justify-center p-4 backdrop-blur-sm z-50">
      <div class="bg-gray-800 rounded-xl p-6 w-full max-w-md border border-gray-700 shadow-2xl">
        <h3 class="text-xl font-bold mb-4">Новый пользователь</h3>
        <div class="space-y-4">
          <div>
            <label class="block text-sm text-gray-400 mb-1">Имя (Англ, без пробелов)</label>
            <input v-model="newUserName" type="text" class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-2 text-white focus:outline-none focus:border-blue-500 transition-colors" placeholder="user_123">
          </div>
          <div>
            <label class="block text-sm text-gray-400 mb-1">Лимит IP (0 = Безлимит)</label>
            <input v-model="newUserLimit" type="number" class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-2 text-white focus:outline-none focus:border-blue-500 transition-colors" min="0">
          </div>
        </div>
        <div class="mt-6 flex justify-end gap-3">
          <button @click="showUserModal = false" class="px-4 py-2 text-gray-400 hover:text-white transition-colors">Отмена</button>
          <button @click="addUser" :disabled="isSubmittingUser || !newUserName" class="bg-blue-600 hover:bg-blue-500 disabled:bg-gray-600 text-white px-4 py-2 rounded-lg font-medium transition-colors">
            {{ isSubmittingUser ? 'Создаем...' : 'Сохранить' }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="showNodeModal" class="fixed inset-0 bg-black/60 flex items-center justify-center p-4 backdrop-blur-sm z-50">
      <div class="bg-gray-800 rounded-xl p-6 w-full max-w-md border border-gray-700 shadow-2xl">
        <h3 class="text-xl font-bold mb-4 text-indigo-400">🤖 Авто-деплой сервера</h3>
        
        <div v-if="deployMessage" class="bg-indigo-900/50 border border-indigo-500/50 text-indigo-200 p-4 rounded-lg mb-4 text-sm">
          {{ deployMessage }}
        </div>

        <div v-else class="space-y-4">
          <div>
            <label class="block text-sm text-gray-400 mb-1">IP Адрес сервера</label>
            <input v-model="newNodeIP" type="text" class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-2 text-white focus:outline-none focus:border-indigo-500 transition-colors" placeholder="192.168.1.1">
          </div>
          
          <div>
            <label class="block text-sm text-gray-400 mb-1">Роль узла в кластере</label>
            <select v-model="newNodeType" class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-2 text-white focus:outline-none focus:border-indigo-500 transition-colors">
              <option value="ru_bridge">🇷🇺 RU Мост (Входная точка)</option>
              <option value="eu_exit">🇪🇺 EU Exit (Выход в мир + WARP)</option>
            </select>
          </div>

          <div>
            <label class="block text-sm text-gray-400 mb-1">Root Пароль (только для установки)</label>
            <input v-model="newNodePassword" type="password" class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-2 text-white focus:outline-none focus:border-indigo-500 transition-colors" placeholder="••••••••">
          </div>
        </div>

        <div class="mt-6 flex justify-end gap-3">
          <button v-if="!deployMessage" @click="showNodeModal = false" class="px-4 py-2 text-gray-400 hover:text-white transition-colors">Отмена</button>
          <button v-if="!deployMessage" @click="deployNode" :disabled="isDeploying || !newNodeIP || !newNodePassword" class="bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 text-white px-4 py-2 rounded-lg font-medium transition-colors shadow-lg">
            {{ isDeploying ? 'Подключение...' : 'Начать Деплой' }}
          </button>
        </div>
      </div>
    </div>

  </div>
</template>

<style>
/* Плавное появление вкладок */
.animate-fade-in {
  animation: fadeIn 0.2s ease-in-out;
}
@keyframes fadeIn {
  from { opacity: 0; transform: translateY(5px); }
  to { opacity: 1; transform: translateY(0); }
}
</style>