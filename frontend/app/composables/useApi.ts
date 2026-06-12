import type { ApiResponse } from '~/types/api'

export function useApi() {
  const auth = useAuthStore()
  const toast = useToast()

  async function request<T = unknown>(
    url: string,
    options: RequestInit = {}
  ): Promise<T> {
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...(options.headers as Record<string, string>)
    }

    if (auth.token) {
      headers['Authorization'] = `Bearer ${auth.token}`
    }

    try {
      const response = await fetch(url, {
        ...options,
        headers
      })

      if (!response.ok) {
        const body = await response.json().catch(() => ({})) as ApiResponse
        const msg = body.message || `HTTP ${response.status}`
        if (response.status === 401) {
          auth.logout()
          navigateTo('/login')
        }
        throw new Error(msg)
      }

      return await response.json() as T
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '请求失败'
      toast.add({ title: '错误', description: message, color: 'error' })
      throw err
    }
  }

  async function apiGet<T = unknown>(url: string): Promise<T> {
    return request<T>(url)
  }

  async function apiPost<T = unknown>(url: string, body?: unknown): Promise<T> {
    return request<T>(url, {
      method: 'POST',
      body: body ? JSON.stringify(body) : undefined
    })
  }

  async function apiPut<T = unknown>(url: string, body?: unknown): Promise<T> {
    return request<T>(url, {
      method: 'PUT',
      body: body ? JSON.stringify(body) : undefined
    })
  }

  async function apiPatch<T = unknown>(url: string, body?: unknown): Promise<T> {
    return request<T>(url, {
      method: 'PATCH',
      body: body ? JSON.stringify(body) : undefined
    })
  }

  async function apiDelete<T = unknown>(url: string, body?: unknown): Promise<T> {
    return request<T>(url, {
      method: 'DELETE',
      body: body ? JSON.stringify(body) : undefined
    })
  }

  return { apiGet, apiPost, apiPut, apiPatch, apiDelete }
}
