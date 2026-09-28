import { createHash } from 'node:crypto'
import { closeSync, constants, fstatSync, fsyncSync, ftruncateSync, mkdirSync, openSync, readFileSync, writeSync } from 'node:fs'
import { dirname } from 'node:path'

const digest = text => createHash('sha256').update(text).digest('hex')

/**
 * T-00 single-writer append log. The host must exclusively own the generation.
 * Complete corrupt records fail closed; only an incomplete trailing write can
 * be discarded. Appends are acknowledged only after fsync. This is not a
 * replacement for the upstream Session log or a multi-process lock service.
 */
export class DurableLog {
  #fd
  #records = []
  #failed = false

  constructor(path) {
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 })
    this.#fd = openSync(path, constants.O_CREAT | constants.O_RDWR | constants.O_APPEND | constants.O_NOFOLLOW, 0o600)
    try {
      if (!fstatSync(this.#fd).isFile()) throw new Error('agent_invalid_journal')
      const bytes = readFileSync(this.#fd)
      const end = bytes.lastIndexOf(10) + 1
      const lines = bytes.subarray(0, end).toString('utf8').split('\n').filter(Boolean)
      for (const line of lines) {
        const record = JSON.parse(line)
        if (record.sequence !== this.#records.length + 1 || record.checksum !== digest(JSON.stringify(record.data))) {
          throw new Error('agent_journal_corrupt')
        }
        this.#records.push(record.data)
      }
      if (end !== bytes.length) { ftruncateSync(this.#fd, end); fsyncSync(this.#fd) }
      const directory = openSync(dirname(path), constants.O_RDONLY)
      try { fsyncSync(directory) } finally { closeSync(directory) }
    } catch {
      closeSync(this.#fd)
      throw new Error('agent_journal_corrupt')
    }
  }

  get records() { return structuredClone(this.#records) }

  append(data) {
    if (this.#failed || this.#fd === undefined) throw new Error('agent_journal_unavailable')
    const encoded = JSON.stringify(data)
    const snapshot = JSON.parse(encoded)
    const line = Buffer.from(JSON.stringify({ sequence: this.#records.length + 1, data: snapshot, checksum: digest(encoded) }) + '\n')
    try {
      let offset = 0
      while (offset < line.length) {
        const written = writeSync(this.#fd, line, offset, line.length - offset)
        if (!written) throw new Error('agent_journal_write_failed')
        offset += written
      }
      fsyncSync(this.#fd)
      this.#records.push(snapshot)
    } catch {
      this.#failed = true
      throw new Error('agent_journal_write_failed')
    }
    return structuredClone(snapshot)
  }

  close() {
    if (this.#fd === undefined) return
    closeSync(this.#fd)
    this.#fd = undefined
  }
}
