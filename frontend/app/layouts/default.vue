<script setup lang="ts">
const auth = useAuthStore()
const colorMode = useColorMode()

const isDark = computed({
  get: () => colorMode.value === 'dark',
  set: (value) => {
    colorMode.preference = value ? 'dark' : 'light'
  }
})
</script>

<template>
  <UApp>
    <div class="min-h-screen flex flex-col">
      <header class="border-b border-gray-200 dark:border-gray-800">
        <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 h-16 flex items-center justify-between">
          <NuxtLink to="/" class="flex items-center gap-2 font-bold text-xl">
            <UIcon name="i-lucide-route" class="size-6 text-primary" />
            RouterX
          </NuxtLink>
          <div class="flex items-center gap-4">
            <UButton
              :icon="isDark ? 'i-lucide-sun' : 'i-lucide-moon'"
              color="neutral"
              variant="ghost"
              @click="isDark = !isDark"
            />
            <template v-if="auth.isLoggedIn">
              <UDropdownMenu
                :items="[[
                  { label: '控制台', to: auth.isAdmin ? '/admin' : '/dashboard' },
                  { label: '退出登录', onSelect: () => auth.logout() }
                ]]"
              >
                <UButton color="neutral" variant="ghost">
                  {{ auth.user?.display_name || auth.user?.username }}
                  <template #trailing>
                    <UIcon name="i-lucide-chevron-down" class="size-4" />
                  </template>
                </UButton>
              </UDropdownMenu>
            </template>
            <template v-else>
              <UButton to="/login" variant="ghost" size="sm">登录</UButton>
              <UButton to="/register" size="sm">注册</UButton>
            </template>
          </div>
        </div>
      </header>
      <main class="flex-1">
        <slot />
      </main>
      <footer class="border-t border-gray-200 dark:border-gray-800 py-4 text-center text-sm text-gray-500">
        RouterX &copy; {{ new Date().getFullYear() }} &mdash; Open-source AI Model Gateway
      </footer>
    </div>
  </UApp>
</template>
