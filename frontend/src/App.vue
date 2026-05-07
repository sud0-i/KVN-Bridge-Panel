<script setup>
import { ref, onMounted } from 'vue'

// --- АВТОРИЗАЦИЯ ---
const token = ref(localStorage.getItem('token') || '')
const loginPassword = ref('')
const loginError = ref('')
const isLoggingIn = ref(false)

const login = async () => {
  isLoggingIn.value = true
  loginError.value = ''
  try {
    const res = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: loginPassword.value })
    })
    
    if (!res.ok) throw new Error('Неверный пароль')
    
    const data = await res.json()
    token.value = data.token
    localStorage.setItem('token', data.token)
    loginPassword.value = ''
    
    // Загружаем данные после успешного входа
    fetchUsers()
    fetchNodes()
  } catch (e) {
    loginError.value = e.message
  } finally {
    isLoggingIn.value = false
  }
}

const logout = () => {
  token.value = ''
  localStorage.removeItem('token')
}

// --- УМНЫЙ ЗАПРОС К API (с токеном) ---
const apiCall = async (url, options = {}) => {
  const headers = {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token.value}`,
    ...options.headers
  }
  
  const res = await fetch(url, { ...options, headers })
  
  if (res.status === 401) {
    logout() // Если токен истек или неверный - разлогиниваем
    throw new Error('Сессия истекла')
  }
  return res
}

// --- ГЛОБАЛЬНОЕ СОСТОЯНИЕ ---
const currentTab = ref('users') 

// --- СОСТОЯНИЕ ПОЛЬЗОВАТЕЛЕЙ ---
const users = ref([])
const loadingUsers = ref(false)
const showUserModal = ref(false)
const newUserName = ref('')
const newUserLimit = ref(5)
const isSubmittingUser = ref(false)

// --- СОСТОЯНИЕ НОД (СЕРВЕРОВ) ---
const nodes = ref([])
const loadingNodes = ref(false)
const showNodeModal = ref(false)
const newNodeIP = ref('')
const newNodeType = ref('ru_bridge')
const newNodePassword = ref('')
const isDeploying = ref(false)
const deployMessage = ref('') 

// ================= ФУНКЦИИ ПОЛЬЗОВАТЕЛЕЙ =================
const fetchUsers = async () => {
  if (!token.value) return
  loadingUsers.value = true
  try {
    const res = await apiCall('/api/users')
    users.value = await res.json()
  } catch (error) {
    console.error(error)
  } finally {
    loadingUsers.value = false
  }
}

const addUser = async () => {
  if (!newUserName.value) return
  isSubmittingUser.value = true
  try {
    await apiCall('/api/users', {
      method: 'POST',
      body: JSON.stringify({ name: newUserName.value, ip_limit: newUserLimit.value })
    })
    showUserModal.value = false
    newUserName.value = ''
    newUserLimit.value = 5
    await fetchUsers()
  } catch (error) {
    console.error(error)
  } finally {
    isSubmittingUser.value = false
  }
}

const toggleUserStatus = async (user) => {
  const newStatus = user.Status === 'active' ? 'blocked' : 'active'
  try {
    await apiCall(`/api/users/${user.ID}/status`, {
      method: 'PATCH',
      body: JSON.stringify({ status: newStatus })
    })
    await fetchUsers()
  } catch (error) {
    console.error(error)
  }
}

const deleteUser = async (id) => {
  if (!confirm('Удалить пользователя безвозвратно?')) return
  try {
    await apiCall(`/api/users/${id}`, { method: 'DELETE' })
    await fetchUsers()
  } catch (error) {
    console.error(error)
  }
}

const copySubLink = (uuid) => {
  const baseUrl = window.location.origin
  const link = `${baseUrl}/sub/${uuid}`
  navigator.clipboard.writeText(link)
  alert('Ссылка скопирована: ' + link)
}

// ================= ФУНКЦИИ НОД =================
const fetchNodes = async () => {
  if (!token.value) return
  loadingNodes.value = true
  try {
    const res = await apiCall('/api/nodes')
    nodes.value = await res.json()
  } catch (error) {
    console.error(error)
  } finally {
    loadingNodes.value = false
  }
}

const deployNode = async () => {
  if (!newNodeIP.value || !newNodePassword.value) return
  isDeploying.value = true
  deployMessage.value = ''
  
  try {
    const res = await apiCall('/api/nodes', {
      method: 'POST',
      body: JSON.stringify({ ip: newNodeIP.value, type: newNodeType.value, password: newNodePassword.value })
    })
    const data = await res.json()
    deployMessage.value = data.message
    
    newNodeIP.value = ''
    newNodePassword.value = ''
    setTimeout(() => {
      showNodeModal.value = false
      deployMessage.value = ''
      fetchNodes() 
    }, 3000)
  } catch (error) {
    console.error(error)
  } finally {
    isDeploying.value = false
  }
}

onMounted(() => {
  if (token.value) {
    fetchUsers()
    fetchNodes()
  }
})
</script>

<template>
  <div v-if="!token" class="min-h-screen flex items-center justify-center p-4 bg-gray-900 relative overflow-hidden">
    <div class="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-96 h-96 bg-blue-600/20 blur-[100px] rounded-full pointer-events-none"></div>
    
    <div class="bg-gray-800 p-8 rounded-2xl shadow-2xl w-full max-w-sm border border-gray-700 relative z-10 animate-fade-in">
      <div class="text-center mb-8">
        <h1 class="text-3xl font-bold text-blue-400 mb-2">KVN Panel</h1>
        <p class="text-gray-400 text-sm">Введите пароль администратора</p>
      </div>

      <form @submit.prevent="login" class="space-y-4">
        <div>
          <input v-model="loginPassword" type="password" placeholder="••••••••" 
            class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-3 text-white focus:outline-none focus:border-blue-500 transition-colors text-center text-lg tracking-widest"
            required>
        </div>
        
        <p v-if="loginError" class="text-red-400 text-sm text-center">{{ loginError }}</p>

        <button type="submit" :disabled="isLoggingIn || !loginPassword" 
          class="w-full bg-blue-600 hover:bg-blue-500 disabled:bg-gray-700 text-white font-bold py-3 px-4 rounded-lg transition-colors shadow-lg shadow-blue-900/20">
          {{ isLoggingIn ? 'Проверка...' : 'Войти' }}
        </button>
      </form>
    </div>
  </div>

  <div v-else class="min-h-screen p-8 relative animate-fade-in">
    <div class="max-w-5xl mx-auto">
      
      <div class="flex justify-between items-end mb-8 border-b border-gray-700 pb-4">
        <h1 class="text-3xl font-bold text-blue-400">🚀 VPN Cluster</h1>
        <div class="flex items-center gap-4">
          <div class="flex gap-2">
            <button @click="currentTab = 'users'" :class="['px-4 py-2 rounded-t-lg font-medium transition-colors border-b-2', currentTab === 'users' ? 'border-blue-500 text-blue-400 bg-gray-800' : 'border-transparent text-gray-400 hover:text-gray-200']">👥 Пользователи</button>
            <button @click="currentTab = 'nodes'" :class="['px-4 py-2 rounded-t-lg font-medium transition-colors border-b-2', currentTab === 'nodes' ? 'border-blue-500 text-blue-400 bg-gray-800' : 'border-transparent text-gray-400 hover:text-gray-200']">🖥 Серверы</button>
          </div>
          <button @click="logout" class="text-sm text-gray-400 hover:text-red-400 transition-colors ml-4 px-3 py-1 border border-gray-700 rounded-lg hover:border-red-500/50">
            Выйти
          </button>
        </div>
      </div>
      
      <div v-if="currentTab === 'users'" class="bg-gray-800 rounded-xl shadow-lg p-6 border border-gray-700">
        <div class="flex justify-between items-center mb-6">
          <h2 class="text-xl font-semibold">Список клиентов</h2>
          <button @click="showUserModal = true" class="bg-blue-600 hover:bg-blue-500 text-white px-4 py-2 rounded-lg font-medium transition-colors">
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
              <th class="py-3 font-medium text-right">Действия</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="users.length === 0">
              <td colspan="5" class="py-8 text-center text-gray-500">Нет пользователей. Создайте первого!</td>
            </tr>
            <tr v-for="user in users" :key="user.ID" class="border-b border-gray-700/50 hover:bg-gray-700/20 transition-colors">
              <td class="py-3 font-medium">{{ user.Name }}</td>
              <td class="py-3">{{ user.IPLimit === 0 ? 'Безлимит' : user.IPLimit }}</td>
              <td class="py-3 text-sm text-gray-400">
                <span class="text-green-400">↓{{ (user.TrafficDown / 1073741824).toFixed(2) }} GB</span> / 
                <span class="text-blue-400">↑{{ (user.TrafficUp / 1073741824).toFixed(2) }} GB</span>
              </td>
              <td class="py-3">
                <span :class="['px-2 py-1 rounded-full text-xs font-medium', user.Status === 'active' ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400']">
                  {{ user.Status === 'active' ? 'Активен' : 'Заблокирован' }}
                </span>
              </td>
              <td class="py-3 text-right">
                <div class="flex gap-3 justify-end">
                  <button @click="toggleUserStatus(user)" :title="user.Status === 'active' ? 'Заблокировать' : 'Разблокировать'" class="text-gray-400 hover:text-orange-400 transition-colors text-lg">
                    <span v-if="user.Status === 'active'">⏸️</span><span v-else>▶️</span>
                  </button>
                  <button @click="copySubLink(user.ID)" title="Скопировать ссылку" class="text-gray-400 hover:text-blue-400 transition-colors text-lg">🔗</button>
                  <button @click="deleteUser(user.ID)" title="Удалить" class="text-gray-400 hover:text-red-500 transition-colors text-lg">🗑️</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="currentTab === 'nodes'" class="bg-gray-800 rounded-xl shadow-lg p-6 border border-gray-700">
        <div class="flex justify-between items-center mb-6">
          <h2 class="text-xl font-semibold">Инфраструктура кластера</h2>
          <button @click="showNodeModal = true" class="bg-indigo-600 hover:bg-indigo-500 text-white px-4 py-2 rounded-lg font-medium transition-colors shadow-lg">
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
                <span v-else class="bg-yellow-500/20 text-yellow-400 px-2 py-1 rounded-full text-xs font-medium animate-pulse">Офлайн</span>
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
            Сохранить
          </button>
        </div>
      </div>
    </div>

    <div v-if="showNodeModal" class="fixed inset-0 bg-black/60 flex items-center justify-center p-4 backdrop-blur-sm z-50">
      <div class="bg-gray-800 rounded-xl p-6 w-full max-w-md border border-gray-700 shadow-2xl">
        <h3 class="text-xl font-bold mb-4 text-indigo-400">🤖 Авто-деплой сервера</h3>
        <div v-if="deployMessage" class="bg-indigo-900/50 border border-indigo-500/50 text-indigo-200 p-4 rounded-lg mb-4 text-sm">{{ deployMessage }}</div>
        <div v-else class="space-y-4">
          <div>
            <label class="block text-sm text-gray-400 mb-1">IP Адрес</label>
            <input v-model="newNodeIP" type="text" class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-2 text-white focus:outline-none focus:border-indigo-500 transition-colors">
          </div>
          <div>
            <label class="block text-sm text-gray-400 mb-1">Роль узла</label>
            <select v-model="newNodeType" class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-2 text-white focus:outline-none focus:border-indigo-500 transition-colors">
              <option value="ru_bridge">🇷🇺 RU Мост</option>
              <option value="eu_exit">🇪🇺 EU Exit</option>
            </select>
          </div>
          <div>
            <label class="block text-sm text-gray-400 mb-1">Root Пароль</label>
            <input v-model="newNodePassword" type="password" class="w-full bg-gray-900 border border-gray-700 rounded-lg px-4 py-2 text-white focus:outline-none focus:border-indigo-500 transition-colors">
          </div>
        </div>
        <div class="mt-6 flex justify-end gap-3">
          <button v-if="!deployMessage" @click="showNodeModal = false" class="px-4 py-2 text-gray-400 hover:text-white transition-colors">Отмена</button>
          <button v-if="!deployMessage" @click="deployNode" :disabled="isDeploying || !newNodeIP || !newNodePassword" class="bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 text-white px-4 py-2 rounded-lg font-medium transition-colors">
            Начать Деплой
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style>
.animate-fade-in { animation: fadeIn 0.3s ease-out; }
@keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
</style>