export default defineNuxtRouteMiddleware((to) => {
  const auth = useAuthStore()

  if (!auth.isAdmin) {
    return navigateTo('/dashboard')
  }
})
