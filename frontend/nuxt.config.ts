export default defineNuxtConfig({
  ssr: false,

  app: {
    baseURL: '/',
    head: {
      htmlAttrs: { lang: 'zh-CN' },
      link: [{ rel: 'icon', href: '/favicon.ico' }]
    }
  },

  modules: ['@nuxt/ui', '@pinia/nuxt'],

  css: ['~/assets/css/main.css'],

  nitro: {
    preset: 'static'
  },

  devtools: { enabled: true },

  devServer: {
    port: 5173
  },

  vite: {
    server: {
      proxy: {
        '/v0': {
          target: 'http://localhost:3000',
          changeOrigin: true
        },
        '/v1': {
          target: 'http://localhost:3000',
          changeOrigin: true
        },
        '/health': {
          target: 'http://localhost:3000',
          changeOrigin: true
        }
      }
    }
  },
  compatibilityDate: '2025-12-01'
})
