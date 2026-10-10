import { onMounted, onScopeDispose, watch, type Ref } from 'vue'

/**
 * Lifecycle-safe, completion-based polling. Slow requests never overlap;
 * a manual refresh while one is running is coalesced into one follow-up.
 */
export function usePolling(task: () => Promise<void>, intervalSeconds: Ref<number>) {
  let timer: ReturnType<typeof setTimeout> | null = null
  let running = false
  let rerun = false
  let disposed = false

  function clearTimer() {
    if (timer) clearTimeout(timer)
    timer = null
  }

  function schedule() {
    clearTimer()
    const seconds = intervalSeconds.value
    if (!disposed && seconds > 0) {
      timer = setTimeout(run, seconds * 1000)
    }
  }

  async function run() {
    clearTimer()
    if (disposed) return
    if (running) {
      rerun = true
      return
    }
    running = true
    try {
      await task()
    } finally {
      running = false
      if (rerun) {
        rerun = false
        await run()
      } else {
        schedule()
      }
    }
  }

  watch(intervalSeconds, schedule)
  onMounted(run)
  onScopeDispose(() => {
    disposed = true
    rerun = false
    clearTimer()
  })

  return { refresh: run }
}

