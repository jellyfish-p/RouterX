export default defineNuxtRouteMiddleware((to) => {
  const auth = useAuthStore()

  if (auth.isLoggedIn) {
    if (auth.isAdmin) {
      return navigateTo('/admin')
    }
    return navigateTo('/dashboard')
  }
})
