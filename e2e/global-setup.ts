import { rmSync } from 'node:fs'

// Wipe the ephemeral SQLite DB before each run so every e2e starts from a clean
// monitor (no servers/targets left over from a previous run). The monitor
// recreates the directory + schema on boot.
export default async function globalSetup() {
  rmSync('/tmp/fm-e2e', { recursive: true, force: true })
}
