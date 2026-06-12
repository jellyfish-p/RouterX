<script setup lang="ts">
const auth = useAuthStore()
const route = useRoute()

const pageTitle = computed(() => (route.meta.title as string) || '')

const sidebarItems = computed(() => {
  if (auth.isAdmin) {
    return [
      { label: '仪表盘', icon: 'i-lucide-layout-dashboard', to: '/admin' },
      { label: '用户管理', icon: 'i-lucide-users', to: '/admin/users' },
      { label: '管理员', icon: 'i-lucide-shield', to: '/admin/admins' },
      { label: '通道管理', icon: 'i-lucide-network', to: '/admin/channels' },
      { label: '调用日志', icon: 'i-lucide-scroll-text', to: '/admin/logs' },
      { label: '系统设置', icon: 'i-lucide-settings', to: '/admin/settings' },
    ]
  }
  return [
    { label: '概览', icon: 'i-lucide-layout-dashboard', to: '/dashboard' },
    { label: '个人信息', icon: 'i-lucide-user', to: '/dashboard/profile' },
    { label: 'API 令牌', icon: 'i-lucide-key', to: '/dashboard/tokens' },
    { label: '调用日志', icon: 'i-lucide-scroll-text', to: '/dashboard/logs' },
    { label: '账单', icon: 'i-lucide-credit-card', to: '/dashboard/billing' },
  ]
})
</script>

<template>
  <UApp>
    <div class="flex h-screen overflow-hidden">
      <aside class="w-64 border-r border-gray-200 dark:border-gray-800 flex flex-col flex-shrink-0">
        <div class="h-16 flex items-center px-4 border-b border-gray-200 dark:border-gray-800">
          <NuxtLink to="/" class="flex items-center gap-2 font-bold text-xl">
            <UIcon name="i-lucide-route" class="size-6 text-primary" />
            RouterX
          </NuxtLink>
        </div>
        <nav class="flex-1 overflow-y-auto p-4 space-y-1">
          <UVerticalNavigation :links="sidebarItems" />
        </nav>
        <div class="p-4 border-t border-gray-200 dark:border-gray-800">
          <div class="flex items-center gap-3">
            <UAvatar :text="(auth.user?.display_name || auth.user?.username || '?').charAt(0)" size="sm" />
            <div class="flex-1 min-w-0">
              <p class="text-sm font-medium truncate">{{ auth.user?.display_name || auth.user?.username }}</p>
              <p class="text-xs text-gray-500 truncate">{{ auth.isSuperAdmin ? '超级管理员' : auth.isAdmin ? '管理员' : '用户' }}</p>
            </div>
            <UButton
              icon="i-lucide-log-out"
              color="neutral"
              variant="ghost"
              size="xs"
              @click="auth.logout()"
            />
          </div>
        </div>
      </aside>
      <div class="flex-1 flex flex-col overflow-hidden">
        <header class="h-16 border-b border-gray-200 dark:border-gray-800 flex items-center justify-between px-6">
          <div class="text-xl font-semibold">
            {{ pageTitle }}
          </div>
          <div class="flex items-center gap-3">
            <UColorModeButton />
          </div>
        </header>
        <main class="flex-1 overflow-y-auto p-6">
          <slot />
        </main>
      </div>
    </div>
  </UApp>
</template>
