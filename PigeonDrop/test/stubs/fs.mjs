import fs from 'fs'; import path from 'path'
export const O_RDONLY = 0
// maxFds / failOpen / failStat / failWhole model device behaviour: a small descriptor table, an open that
// fails, a stat that fails, a whole-file read that fails.
export const ctl = { root: null, shortRead: null, readCalls: 0, maxFds: 4, failOpen: null, failStat: null, failWhole: null, failWrite: null, failRm: null, failList: false, strictClose: true }
const abs = (p) => path.join(ctl.root, p)
export const readdirSync = ({ path: p }) => { if (ctl.failList) throw new Error('cannot list'); return fs.existsSync(abs(p)) ? fs.readdirSync(abs(p)) : [] }
export const mkdirSync = ({ path: p }) => { if (fs.existsSync(abs(p))) throw new Error('exists'); fs.mkdirSync(abs(p), { recursive: true }) }
export const readFileSync = ({ path: p, options }) => {
  if (!fs.existsSync(abs(p))) return undefined
  if (options && options.encoding) return fs.readFileSync(abs(p), 'utf8')
  if (ctl.failWhole && ctl.failWhole(p)) return undefined
  const b = fs.readFileSync(abs(p)); return b.buffer.slice(b.byteOffset, b.byteOffset + b.length)   // ArrayBuffer, like the device
}
export const writeFileSync = ({ path: p, data }) => { if (ctl.failWrite && ctl.failWrite(p)) throw new Error('no space left'); fs.writeFileSync(abs(p), data) }
// failRm models a delete that does not work: 'throw' raises, 'return' answers with an error code and leaves the file.
export const rmSync = ({ path: p }) => {
  const how = ctl.failRm && ctl.failRm(p)
  if (how === 'throw') throw new Error('cannot remove')
  if (how) return -1
  fs.rmSync(abs(p), { force: true }); return 0
}
export const statSync = ({ path: p }) => (fs.existsSync(abs(p)) && !(ctl.failStat && ctl.failStat(p)) ? { size: fs.statSync(abs(p)).size } : undefined)
export const fds = new Map(); let next = 3
export const resetFds = () => { for (const h of fds.values()) fs.closeSync(h); fds.clear() }
export const openSync = ({ path: p }) => {
  if (fds.size >= ctl.maxFds || (ctl.failOpen && ctl.failOpen(p))) return undefined
  const fd = next++; fds.set(fd, fs.openSync(abs(p), 'r')); return fd
}
// The documented form is closeSync({ fd }). A bare number is not accepted here, so a caller using it leaks the descriptor.
export const closeSync = (arg) => {
  const fd = arg && typeof arg === 'object' ? arg.fd : (ctl.strictClose ? undefined : arg)
  if (!fds.has(fd)) return -1
  fs.closeSync(fds.get(fd)); fds.delete(fd); return 0
}
export const readSync = ({ fd, buffer, options }) => {
  ctl.readCalls++
  const view = new Uint8Array(buffer)
  let n = fs.readSync(fds.get(fd), view, options.offset, options.length, options.position)
  if (ctl.shortRead && ctl.shortRead(options.position / options.length)) n = Math.max(0, n - 7)
  return n
}
