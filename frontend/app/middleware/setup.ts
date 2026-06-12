export default defineNuxtRouteMiddleware(async (to) => {
  const auth = useAuthStore()

  if (auth.initialized === null) {
    await auth.checkInitStatus()
  }

  if (!auth.isSystemReady && to.path !== '/setup') {
    return navigateTo('/setup')
  }

  if (auth.isSystemReady && to.path === '/setup') {
    return navigateTo('/login')
  }
})
