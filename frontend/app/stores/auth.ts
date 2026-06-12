import { defineStore } from 'pinia'
import type { UserBrief, InitStatus, ApiResponse } from '~/types/api'

interface AuthState {
  token: string | null
  user: UserBrief | null
  initialized: boolean | null
  loading: boolean
}

export const useAuthStore = defineStore('auth', {
  state: (): AuthState => ({
    token: null,
    user: null,
    initialized: null,
    loading: false
  }),

  getters: {
    isLoggedIn: (state) => !!state.token && !!state.user,
    isAdmin: (state) => {
      if (!state.user) return false
      return state.user.role >= 1
    },
    isSuperAdmin: (state) => {
      if (!state.user) return false
      return state.user.role >= 2
    },
    isSystemReady: (state) => state.initialized === true
  },

  actions: {
    async checkInitStatus(): Promise<boolean> {
      try {
        const res = await fetch('/v0/setup/status')
        const body = await res.json() as ApiResponse<InitStatus>
        this.initialized = body.data?.initialized ?? false
        return this.initialized
      } catch {
        this.initialized = false
        return false
      }
    },

    login(jwt: string, user: UserBrief) {
      this.token = jwt
      this.user = user
      if (import.meta.client) {
        localStorage.setItem('routerx_token', jwt)
        localStorage.setItem('routerx_user', JSON.stringify(user))
      }
    },

    logout() {
      this.token = null
      this.user = null
      if (import.meta.client) {
        localStorage.removeItem('routerx_token')
        localStorage.removeItem('routerx_user')
      }
      navigateTo('/login')
    },

    restoreSession() {
      if (!import.meta.client) return
      const token = localStorage.getItem('routerx_token')
      const userStr = localStorage.getItem('routerx_user')
      if (token && userStr) {
        try {
          this.token = token
          this.user = JSON.parse(userStr)
        } catch {
          this.logout()
        }
      }
    }
  }
})
