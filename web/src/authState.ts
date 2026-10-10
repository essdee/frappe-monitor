import { ref } from 'vue'
import { whoami } from './api'
import { setDisplayTimezone } from './timeDisplay'

export const authChecked = ref(false)
export const authEnabled = ref(false)
export const authenticated = ref(false)

export async function refreshAuth() {
  const status = await whoami()
  authEnabled.value = status.auth_enabled
  authenticated.value = status.authenticated
  setDisplayTimezone(status.timezone)
  authChecked.value = true
  return status
}

export function markAuthenticated(value: boolean) {
  authenticated.value = value
  authChecked.value = true
}
