export default defineNuxtRouteMiddleware(async (to) => {
  const auth = useAuthStore()

  if (!auth.isLoggedIn) {
    auth.restoreSession()
  }

  if (!auth.isLoggedIn) {
    return navigateTo('/login')
  }
})
