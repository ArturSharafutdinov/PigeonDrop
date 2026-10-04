// Main screen of the app. Lets the user record a voice message with the
// watch's hardware "shortcut" button, sends every finished recording to the
// paired phone, and gives quick access to the full recordings list.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { onKey, KEY_SHORTCUT, KEY_EVENT_CLICK, KEY_EVENT_LONG_PRESS } from '@zos/interaction'
import { create, id, codec } from '@zos/media'
import { push } from '@zos/router'
import {
  setPageBrightTime,
  resetPageBrightTime,
  pauseDropWristScreenOff,
  resetDropWristScreenOff,
  pausePalmScreenOff,
  resetPalmScreenOff,
} from '@zos/display'
import { newRecordingPath, listRecordings } from '../utils/recordings'
import {
  pruneRecordings,
  retryPendingUploads,
  pendingRecordings,
  setActiveRecording,
  onUploadState,
} from '../utils/uploadQueue'
import { isPaired } from '../utils/device'
import { getStorageState } from '../utils/storageState'
import { MAX_RECORDING_SECONDS } from '../config'

// How long the screen is kept awake for an upload, so it can be sent to the
// server before the display sleeps and the Mini Program is closed. It is a
// window, not a total: it is renewed for as long as chunks keep being sent (see
// extendUploadHold), so a long queue is not cut off, while a stalled upload still
// lets the screen go dark once nothing has moved for this long.
const UPLOAD_SCREEN_HOLD_MS = 60 * 1000

// Extra time on the page-brightness timeout beyond a hold: the stopwatch ticks
// once a second, so an auto-stop can land a moment after the recording limit.
const SCREEN_SLACK_MS = 5 * 1000

// The warning about the account's storage on the server (utils/storageState.js), shown in its own small label
// under the status text so that it neither replaces nor is replaced by it. The server deletes the oldest
// recordings once the storage is full; this tells the person before and while that happens, and where to
// clear it themselves (the web page opened by pairing). Anything else, 'ok' included, shows nothing.
const STORAGE_NOTICE_COLOR = 0xffb74d
const STORAGE_NOTICES = {
  nearly_full: 'Хранилище почти заполнено. Очистите его на сайте или старые записи начнут удаляться сами',
  full: 'Хранилище заполнено. Очистите его на сайте или старые записи удаляются сами',
}

function storageNoticeText(state) {
  return STORAGE_NOTICES[state] || ''
}

// 75 -> "01:15", 3725 -> "1:02:05". Elapsed time shown on the recording screen.
function formatElapsed(totalSeconds) {
  const pad = (n) => (n < 10 ? '0' + n : '' + n)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  return hours > 0 ? hours + ':' + pad(minutes) + ':' + pad(seconds) : pad(minutes) + ':' + pad(seconds)
}

Page({
  state: {
    recording: false, // true while the Recorder is actively capturing audio
    currentFile: null, // data:// path of the recording currently in progress
    recorder: null, // active Recorder instance, or null when idle
    player: null, // lazily created Player instance, reused across plays (see getPlayer)
    statusText: null, // reference to the on-screen status TEXT widget, so we can update it later
    noticeText: null, // reference to the storage-warning TEXT widget (empty when there is nothing to warn about)
    startedAt: null, // Date.now() when the current recording began (drives the stopwatch)
    ticker: null, // setInterval id of the once-a-second stopwatch refresh, or null when idle
    uploadHoldTimer: null, // setTimeout id that ends the upload screen hold, or null when no hold is active
    uploadHoldUntil: 0, // Date.now() at which the current upload hold runs out
  },

  build() {
    // Status label in the upper part of the screen. Shrunk from the original
    // 230px tall to make room for the extra "Настройки" button below - see
    // the "list" button's placement note for the round-display safe-zone
    // reasoning that governs every button placement on this page.
    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(10),
      w: px(480),
      h: px(250),
      text: this.idleText(),
      text_size: px(28),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    // The storage warning, in the band between the status text and the buttons: up to three lines of small
    // text, kept inside the round display's safe zone like everything else on this page. It shows what the
    // server last said, so it is already there when the app opens (see utils/storageState.js).
    this.state.noticeText = createWidget(widget.TEXT, {
      x: px(40),
      y: px(196),
      w: px(400),
      h: px(74),
      text: storageNoticeText(getStorageState()),
      text_size: px(20),
      color: STORAGE_NOTICE_COLOR,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    // Navigates to the settings page (pairing the watch, clearing memory - see
    // page/settings.js). Stacked above "Список записей" rather than beside
    // it - side-by-side placement leaves much tighter margins near the
    // bezel, while stacking lets this reuse the same proven-safe width/x as
    // the button below it.
    createWidget(widget.BUTTON, {
      x: px(100),
      y: px(270),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: 'Настройки',
      text_size: px(24),
      click_func: () => {
        push({ url: 'page/settings' })
      },
    })

    // Navigates to the full recordings list. Placed low on the screen; on a
    // round display a wide rectangle placed too close to the top/bottom edge
    // gets its corners clipped by the bezel, so this button is kept
    // comfortably within the circle instead of spanning the full width.
    createWidget(widget.BUTTON, {
      x: px(100),
      y: px(350),
      w: px(280),
      h: px(70),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: 'Список записей',
      text_size: px(24),
      click_func: () => {
        push({ url: 'page/list' })
      },
    })

    // The watch only exposes one physical "shortcut" button, so it's
    // overloaded with two gestures: a click toggles recording, a long press
    // replays the most recent message. Returning true tells the system we
    // handled the key ourselves and it shouldn't fall back to its default
    // action (e.g. opening the workout menu).
    onKey({
      callback: (key, keyEvent) => {
        if (key !== KEY_SHORTCUT) return false

        if (keyEvent === KEY_EVENT_CLICK) {
          this.toggleRecording()
          return true
        }

        if (keyEvent === KEY_EVENT_LONG_PRESS) {
          this.playLatestRecording()
          return true
        }

        return false
      },
    })

    // Shows upload progress and failures on the status label, so a recording
    // that doesn't reach the server is visible on the watch instead of failing
    // silently (Side Service logs can't be seen from here).
    onUploadState((state) => this.showUploadState(state))

    // App-open retry trigger for the upload queue - catches anything that
    // was still pending from a previous session (e.g. the watch was out of
    // BLE range or the server was down when it was recorded). Deferred a moment
    // so the screen is drawn first: the first chunk is read and encoded
    // synchronously, and starting it inside build() delayed the whole page.
    setTimeout(() => retryPendingUploads(), 600)
  },

  // Default instructions shown whenever nothing else is going on.
  idleText() {
    return 'Клик — записать\nДолгое нажатие —\nвоспроизвести последнюю'
  },

  toggleRecording() {
    if (this.state.recording) {
      this.stopRecording()
    } else {
      this.startRecording()
    }
  },

  // Starts capturing audio into a brand new, uniquely named file.
  startRecording() {
    // Before the recorder starts, so the display can't time out in the gap. Any
    // hold left over from the previous recording's upload is replaced by this one.
    this.cancelUploadHold()
    this.keepScreenOn(MAX_RECORDING_SECONDS * 1000)

    const filePath = newRecordingPath()
    // Tell the upload queue this file is still being written, so a retry
    // sweep (e.g. triggered by a BLE reconnect mid-recording) can't upload or
    // prune a half-finished recording.
    setActiveRecording(filePath.split('/').pop())
    const recorder = create(id.RECORDER)
    recorder.setFormat(codec.OPUS, { target_file: filePath })
    recorder.start()

    this.state.recorder = recorder
    this.state.currentFile = filePath
    this.state.recording = true
    this.startStopwatch()
  },

  // Stops the current recording, enforces the storage caps (config.js), and
  // kicks off a retry sweep of the upload queue - which now includes this
  // recording as well as any older ones still waiting (utils/uploadQueue.js).
  //
  // limitReached is true when the stop came from the MAX_RECORDING_SECONDS
  // safety limit rather than from the user.
  stopRecording(limitReached) {
    this.stopStopwatch()
    this.state.recorder.stop()
    this.state.recorder = null
    this.state.recording = false
    // The recording is over but the upload isn't: keep the screen on for it.
    this.holdScreenForUpload()
    setActiveRecording(null)
    pruneRecordings()
    retryPendingUploads()

    // Initial status only - the sweep above runs asynchronously, so this shows
    // what was still true as it kicked off. The real progress and the outcome
    // arrive through showUploadState (onUploadState), which also ends the
    // screen hold once everything has been sent.
    const pendingCount = pendingRecordings().length
    if (pendingCount === 0) this.endUploadHold()
    // A watch that is not paired sends nothing: say the recording is safe, and what to do about it.
    const sending = isPaired() ? 'Отправка...\nВ очереди: ' + pendingCount : 'Записано. Часы не\nпривязаны: Настройки\n→ Привязать часы'
    let status
    if (limitReached) {
      status = 'Лимит записи\nдостигнут\n\n' + sending
    } else {
      status = pendingCount > 0 ? sending : 'Отправлено\n\n' + this.idleText()
    }
    this.updateStatus(status)
  },

  // Keeps the display awake for `durationMs`. By default Zepp OS puts the
  // screen to sleep after ~10 s and then closes the Mini Program, which
  // silently killed long recordings - and uploads still in progress - before
  // they were ever sent. Three things can sleep the screen - the page timeout,
  // lowering the wrist, and resting a palm on it - so all three are suspended.
  // Every duration is bounded (by the recording limit while recording, by
  // UPLOAD_SCREEN_HOLD_MS while uploading), so nothing can hold the screen on
  // (and drain the battery) forever; releaseScreen() undoes it all. Calling this
  // again replaces the hold that was active.
  keepScreenOn(durationMs) {
    this.releaseScreen()
    this.checkDisplay('setPageBrightTime', setPageBrightTime({ brightTime: durationMs + SCREEN_SLACK_MS }))
    this.checkDisplay('pauseDropWristScreenOff', pauseDropWristScreenOff({ duration: durationMs }))
    this.checkDisplay('pausePalmScreenOff', pausePalmScreenOff({ duration: durationMs }))
  },

  releaseScreen() {
    resetPageBrightTime()
    resetDropWristScreenOff()
    resetPalmScreenOff()
  },

  // The screen stays on while recordings are being sent - after a recording
  // stops, and also for older ones still queued (sent when the app opens or the
  // phone reconnects), which used to be cut off when the screen went dark. Each
  // hold lasts UPLOAD_SCREEN_HOLD_MS and is renewed by upload progress
  // (extendUploadHold). It ends early once everything is sent (showUploadState)
  // and is replaced if a new recording starts (startRecording).
  holdScreenForUpload() {
    this.cancelUploadHold()
    this.keepScreenOn(UPLOAD_SCREEN_HOLD_MS)
    this.state.uploadHoldUntil = Date.now() + UPLOAD_SCREEN_HOLD_MS
    this.state.uploadHoldTimer = setTimeout(() => this.endUploadHold(), UPLOAD_SCREEN_HOLD_MS)
  },

  // Called for every chunk sent. Starts a hold if none is active, and renews it
  // once less than half of it is left - not on every chunk, which would mean
  // re-arming the display three times a second for no benefit.
  extendUploadHold() {
    if (this.state.uploadHoldTimer !== null && this.state.uploadHoldUntil - Date.now() > UPLOAD_SCREEN_HOLD_MS / 2) return
    this.holdScreenForUpload()
  },

  endUploadHold() {
    this.cancelUploadHold()
    // A recording owns the screen hold while it runs; never release it from here.
    if (!this.state.recording) this.releaseScreen()
  },

  cancelUploadHold() {
    if (this.state.uploadHoldTimer !== null) {
      clearTimeout(this.state.uploadHoldTimer)
      this.state.uploadHoldTimer = null
    }
  },

  // Display calls return 0 on success. A failure doesn't stop the recording,
  // but it is logged because it would bring the old "screen turns off" bug back.
  checkDisplay(name, result) {
    if (result !== 0) {
      console.log('display call failed:', name, result)
    }
  },

  // Stopwatch: shows how long the current recording has been running. Elapsed
  // time is computed from the wall clock rather than by counting ticks, so a
  // late or skipped tick can never make it drift. It also enforces the
  // recording limit.
  startStopwatch() {
    this.state.startedAt = Date.now()
    this.renderStopwatch()
    this.state.ticker = setInterval(() => this.renderStopwatch(), 1000)
  },

  stopStopwatch() {
    if (this.state.ticker !== null) {
      clearInterval(this.state.ticker)
      this.state.ticker = null
    }
  },

  renderStopwatch() {
    if (!this.state.recording) return

    const elapsed = Math.floor((Date.now() - this.state.startedAt) / 1000)
    if (this.state.recording && elapsed >= MAX_RECORDING_SECONDS) {
      this.stopRecording(true)
      return
    }
    this.updateStatus('Идёт запись\n' + formatElapsed(elapsed) + '\nНажмите ещё раз,\nчтобы остановить')
  },

  // The system can still tear the page down mid-recording - the user pressing
  // the side button to sleep the screen, for example. Stop the recorder so the
  // file is closed properly instead of left half-written; it is picked up and
  // sent by the app-open retry in build() the next time the app starts.
  onDestroy() {
    onUploadState(null)
    this.stopStopwatch()
    if (this.state.recording && this.state.recorder) {
      try {
        this.state.recorder.stop()
      } catch (e) {
        console.log('recorder.stop failed in onDestroy:', e)
      }
      this.state.recorder = null
      this.state.recording = false
      this.releaseScreen()
      setActiveRecording(null)
    }
    if (this.state.uploadHoldTimer !== null) this.endUploadHold()
  },

  // Reflects the upload queue on the status label (see onUploadState). The status is ignored
  // while recording, so a background upload can never overwrite the stopwatch.
  showUploadState(state) {
    // The storage warning has a label of its own, so it follows the server whatever the status label is showing,
    // a recording in progress included. "Nothing said" keeps what is shown; a refused token makes it moot, since
    // it was about the account this watch was paired with.
    if (state.state === 'done' && state.storage) this.showStorageNotice(state.storage)
    if (state.state === 'unpaired' && state.revoked) this.showStorageNotice(null)

    if (this.state.recording) return

    if (state.state === 'sending') {
      this.extendUploadHold()
      this.updateStatus('Отправка ' + state.index + ' из ' + state.count + '\nчасть ' + state.chunk + ' из ' + state.total)
    } else if (state.state === 'done') {
      this.updateStatus(state.pending > 0 ? 'В очереди: ' + state.pending : 'Отправлено\n\n' + this.idleText())

      // Everything is sent, so the screen no longer has to be held. After an
      // error it deliberately stays on until the hold runs out, so the reason
      // can be read.
      if (state.pending === 0 && this.state.uploadHoldTimer !== null) this.endUploadHold()
    } else if (state.state === 'error') {
      this.updateStatus('Не удалось отправить\n' + state.message + '\nВ очереди: ' + state.pending)
    } else if (state.state === 'unpaired') {
      // Nothing was sent. The recordings are kept and go out as soon as the watch is paired.
      this.updateStatus(
        (state.revoked ? 'Привязка отозвана' : 'Часы не привязаны') + '\nНастройки →\nПривязать часы\nВ очереди: ' + state.pending,
      )
    }
  },

  // Long-press shortcut: replay whatever was recorded most recently, without
  // having to open the full list.
  playLatestRecording() {
    if (this.state.recording) return

    const recordings = listRecordings()
    if (recordings.length === 0) {
      this.updateStatus('Нет записей для\nвоспроизведения')
      return
    }

    this.playFile(recordings[0].filePath)
  },

  // Lazily creates a single Player instance and reuses it for every playback
  // on this page. Creating a new Player per play used to fail with
  // "cannot read property 'addEventListener' of undefined" on the second
  // call - the hardware only allows one Player alive at a time, and the
  // previous one wasn't released yet. Reusing the same instance and just
  // re-pointing it at a new file (setSource + prepare) avoids that entirely.
  getPlayer() {
    if (!this.state.player) {
      const player = create(id.PLAYER)

      player.addEventListener(player.event.PREPARE, (result) => {
        if (result) {
          player.start()
        } else {
          this.updateStatus('Ошибка чтения\nзаписи\n\n' + this.idleText())
        }
      })

      player.addEventListener(player.event.COMPLETE, () => {
        player.stop()
        this.updateStatus(this.idleText())
      })

      this.state.player = player
    }

    return this.state.player
  },

  // Points the shared Player at a given file and starts the prepare/play flow.
  playFile(filePath) {
    this.updateStatus('Воспроизведение...')
    const player = this.getPlayer()
    player.setSource(player.source.FILE, { file: filePath })
    player.prepare()
  },

  updateStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },

  showStorageNotice(state) {
    this.state.noticeText.setProperty(prop.MORE, { text: storageNoticeText(state) })
  },
})
