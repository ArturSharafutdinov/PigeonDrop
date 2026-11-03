// Main screen of the app. Lets the user record a voice message with the
// watch's hardware "shortcut" button, sends every finished recording to the
// paired phone, and gives quick access to the full recordings list.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { onKey, KEY_SHORTCUT, KEY_EVENT_CLICK, KEY_EVENT_LONG_PRESS } from '@zos/interaction'
import { create, id, codec } from '@zos/media'
import { push } from '@zos/router'
import { holdScreen, releaseScreen } from '../utils/screen'
import { playAudio, detachPlayback } from '../utils/player'
import { newRecordingPath, listRecordings, discardRecording } from '../utils/recordings'
import {
  pruneRecordings,
  retryPendingUploads,
  pendingRecordings,
  setActiveRecording,
  onUploadState,
} from '../utils/uploadQueue'
import { isPaired } from '../utils/device'
import { problemText } from '../utils/linkProblem'
import { t } from '../i18n'
import { MAX_RECORDING_SECONDS } from '../config'

// How long the screen is kept awake for an upload, so it can be sent to the
// server before the display sleeps and the Mini Program is closed. It is a
// window, not a total: it is renewed for as long as chunks keep being sent (see
// extendUploadHold), so a long queue is not cut off, while a stalled upload still
// lets the screen go dark once nothing has moved for this long.
const UPLOAD_SCREEN_HOLD_MS = 60 * 1000

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
    playback: null, // this page's callbacks for the shared Player (utils/player.js), created on the first play
    statusText: null, // reference to the on-screen status TEXT widget, so we can update it later
    settingsButton: null, // the Settings and Recordings buttons are hidden while recording (see showControls)
    listButton: null,
    cancelButton: null, // Cancel: shown only while recording, in the place of the two above
    pendingAlert: null, // a storage warning that arrived during a recording, shown once it ends
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

    // Navigates to the settings page (pairing the watch, clearing memory - see
    // page/settings.js). Stacked above "Список записей" rather than beside
    // it - side-by-side placement leaves much tighter margins near the
    // bezel, while stacking lets this reuse the same proven-safe width/x as
    // the button below it.
    this.state.settingsButton = createWidget(widget.BUTTON, {
      x: px(100),
      y: px(270),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: t('main.settings'),
      text_size: px(24),
      click_func: () => {
        if (this.state.recording) return
        push({ url: 'page/settings' })
      },
    })

    // Navigates to the full recordings list. Placed low on the screen; on a
    // round display a wide rectangle placed too close to the top/bottom edge
    // gets its corners clipped by the bezel, so this button is kept
    // comfortably within the circle instead of spanning the full width.
    this.state.listButton = createWidget(widget.BUTTON, {
      x: px(100),
      y: px(350),
      w: px(280),
      h: px(70),
      radius: px(16),
      normal_color: 0x2f2f2f,
      press_color: 0x4a4a4a,
      text: t('main.list'),
      text_size: px(24),
      click_func: () => {
        if (this.state.recording) return
        push({ url: 'page/list' })
      },
    })

    // Cancels the recording in progress and deletes it: it goes neither to the watch's memory nor to the upload
    // queue. It takes the place of the two buttons above, which are hidden while recording (a third button does
    // not fit the round display), and it is created last so that it lies on top of them. It sits right under
    // the stopwatch, with the same size and place as "Настройки", so it is inside the same safe zone.
    this.state.cancelButton = createWidget(widget.BUTTON, {
      x: px(100),
      y: px(270),
      w: px(280),
      h: px(65),
      radius: px(16),
      normal_color: 0x5c2a2a,
      press_color: 0x7a3a3a,
      text: t('main.cancel'),
      text_size: px(24),
      click_func: () => this.cancelRecording(),
    })
    this.showControls(false)

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
    return t('main.idle')
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
    holdScreen(MAX_RECORDING_SECONDS * 1000)

    const filePath = newRecordingPath()
    const fileName = filePath.split('/').pop()
    // Tell the upload queue this file is still being written, so a retry
    // sweep (e.g. triggered by a BLE reconnect mid-recording) can't upload or
    // prune a half-finished recording.
    setActiveRecording(fileName)
    let recorder
    try {
      recorder = create(id.RECORDER)
      recorder.setFormat(codec.OPUS, { target_file: filePath })
      recorder.start()
    } catch (e) {
      // Typically "Permission Denied device:os.mic" or a recorder that is busy. Nothing is being recorded, so nothing
      // may stay claimed: not the file in the queue, not the screen, and no empty file left behind.
      console.log('recording could not be started:', e)
      setActiveRecording(null)
      releaseScreen()
      discardRecording(fileName)
      this.updateStatus(t('main.start_failed') + '\n\n' + this.idleText())
      return
    }

    this.state.recorder = recorder
    this.state.currentFile = filePath
    this.state.recording = true
    this.showControls(true)
    this.startStopwatch()
  },

  // Stops the current recording, enforces the storage caps (config.js), and
  // kicks off a retry sweep of the upload queue - which now includes this
  // recording as well as any older ones still waiting (utils/uploadQueue.js).
  //
  // limitReached is true when the stop came from the MAX_RECORDING_SECONDS
  // safety limit rather than from the user.
  stopRecording(limitReached) {
    this.endRecording()
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
    const sending = isPaired() ? t('main.sending_wait', { pending: pendingCount }) : t('main.saved_unpaired')
    let status
    if (limitReached) {
      status = t('main.limit_reached') + '\n\n' + sending
    } else {
      status = pendingCount > 0 ? sending : t('main.sent') + '\n\n' + this.idleText()
    }
    this.updateStatus(status)

    // A storage warning that came in while this was being recorded.
    if (this.state.pendingAlert) this.alertStorage(this.state.pendingAlert)
  },

  // The part of ending a recording that stopping and cancelling share: the stopwatch, the recorder, the flag, and
  // the buttons back to the normal ones.
  endRecording() {
    this.stopStopwatch()
    const recorder = this.state.recorder
    // The page's state first: a recorder that fails to stop must not leave it believing a recording is still running.
    // What was written so far is still a recording (the phone trims a cut-off one to its complete packets).
    this.state.recorder = null
    this.state.recording = false
    this.showControls(false)
    try {
      recorder.stop()
    } catch (e) {
      console.log('recorder.stop failed:', e)
    }
  },

  // "Отмена": the recording in progress is thrown away. The file is deleted (the queue and the list never see
  // it: it is the active file until it is gone), no sweep is started and nothing is pruned, since nothing was
  // added. The screen was held for the recording; it is let go, and an upload that is still running takes the
  // hold again with its next chunk (extendUploadHold).
  cancelRecording() {
    if (!this.state.recording) return

    const fileName = this.state.currentFile.split('/').pop()
    this.endRecording()

    if (discardRecording(fileName)) {
      setActiveRecording(null)
      this.endUploadHold()
      this.updateStatus(t('main.cancelled') + '\n\n' + this.idleText())
    } else {
      // The file could not be removed. It stays a recording like any other, and the person is told so rather
      // than left to think it is gone.
      this.holdScreenForUpload()
      setActiveRecording(null)
      pruneRecordings()
      retryPendingUploads()
      this.updateStatus(t('main.cancel_failed'))
    }

    // A storage warning that came in while this was being recorded.
    if (this.state.pendingAlert) this.alertStorage(this.state.pendingAlert)
  },

  // While recording, the screen is the stopwatch and "Отмена"; otherwise the way to settings and the list.
  showControls(recording) {
    this.state.settingsButton.setProperty(prop.VISIBLE, !recording)
    this.state.listButton.setProperty(prop.VISIBLE, !recording)
    this.state.cancelButton.setProperty(prop.VISIBLE, recording)
  },

  // The screen stays on while recordings are being sent - after a recording
  // stops, and also for older ones still queued (sent when the app opens or the
  // phone reconnects), which used to be cut off when the screen went dark. Each
  // hold lasts UPLOAD_SCREEN_HOLD_MS and is renewed by upload progress
  // (extendUploadHold). It ends early once everything is sent (showUploadState)
  // and is replaced if a new recording starts (startRecording). Every hold is
  // bounded (by the recording limit while recording, by UPLOAD_SCREEN_HOLD_MS
  // while uploading), so nothing can keep the screen on and drain the battery
  // forever; see utils/screen.js for what a hold does.
  holdScreenForUpload() {
    this.cancelUploadHold()
    holdScreen(UPLOAD_SCREEN_HOLD_MS)
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
    if (!this.state.recording) releaseScreen()
  },

  cancelUploadHold() {
    if (this.state.uploadHoldTimer !== null) {
      clearTimeout(this.state.uploadHoldTimer)
      this.state.uploadHoldTimer = null
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
    this.updateStatus(t('main.recording', { elapsed: formatElapsed(elapsed) }))
  },

  // The system can still tear the page down mid-recording - the user pressing
  // the side button to sleep the screen, for example. Stop the recorder so the
  // file is closed properly instead of left half-written; it is picked up and
  // sent by the app-open retry in build() the next time the app starts.
  onDestroy() {
    onUploadState(null)
    detachPlayback(this.state.playback)
    this.stopStopwatch()
    if (this.state.recording && this.state.recorder) {
      try {
        this.state.recorder.stop()
      } catch (e) {
        console.log('recorder.stop failed in onDestroy:', e)
      }
      this.state.recorder = null
      this.state.recording = false
      releaseScreen()
      setActiveRecording(null)
    }
    if (this.state.uploadHoldTimer !== null) this.endUploadHold()
  },

  // Reflects the upload queue on the status label (see onUploadState). The status is ignored
  // while recording, so a background upload can never overwrite the stopwatch.
  showUploadState(state) {
    // The storage warning is a page of its own (page/storage.js). It must not interrupt a recording: it waits
    // until the recording has ended.
    if (state.alert) this.alertStorage(state.alert)

    if (this.state.recording) return

    if (state.state === 'connecting') {
      // The phone is being asked whether it is there. A phone that does not answer turns this into the
      // "restart Zepp" error within about 20 seconds, so this text never stays for long.
      this.updateStatus(t('main.connecting', { pending: state.pending }))
    } else if (state.state === 'sending') {
      this.extendUploadHold()
      this.updateStatus(t('main.sending', { index: state.index, count: state.count, chunk: state.chunk, total: state.total }))
    } else if (state.state === 'done') {
      this.updateStatus(state.pending > 0 ? t('main.queued', { pending: state.pending }) : t('main.sent') + '\n\n' + this.idleText())

      // Everything is sent, so the screen no longer has to be held. After an
      // error it deliberately stays on until the hold runs out, so the reason
      // can be read.
      if (state.pending === 0 && this.state.uploadHoldTimer !== null) this.endUploadHold()
    } else if (state.state === 'error') {
      // A broken link between the phone and the watch, or between the phone and the server, is told in words
      // the person can act on (utils/linkProblem.js); any other failure shows its own reason.
      // The reason is added under the text for the server case: the same words cover very different causes.
      const problem = problemText(state.kind)
      const reason = String(state.message || '').replace(/^server: /, '').slice(0, 30)
      this.updateStatus(
        problem
          ? problem + (state.kind === 'server' && reason ? '\n(' + reason + ')' : '') + '\n' + t('main.queued', { pending: state.pending })
          : t('main.send_failed', { message: state.message, pending: state.pending }),
      )
    } else if (state.state === 'unpaired') {
      // Nothing was sent. The recordings are kept and go out as soon as the watch is paired.
      this.updateStatus(
        t(state.revoked ? 'main.pairing_revoked' : 'main.not_paired') + '\n' + t('main.go_pair') + '\n' + t('main.queued', { pending: state.pending }),
      )
    }
  },

  // Long-press shortcut: replay whatever was recorded most recently, without
  // having to open the full list.
  playLatestRecording() {
    if (this.state.recording) return

    const recordings = listRecordings()
    if (recordings.length === 0) {
      this.updateStatus(t('main.no_recordings'))
      return
    }

    this.playFile(recordings[0].filePath)
  },

  // Plays a recording on the app's one shared Player (see utils/player.js for why it
  // is shared with the list page) and keeps the status line in step with it.
  playFile(filePath) {
    this.updateStatus(t('main.playing'))
    if (!this.state.playback) {
      this.state.playback = {
        onError: () => this.updateStatus(t('main.play_failed') + '\n\n' + this.idleText()),
        onComplete: () => this.updateStatus(this.idleText()),
      }
    }
    playAudio(filePath, this.state.playback)
  },

  updateStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },

  // Opens the storage warning page, which buzzes, stays up for a few seconds and goes back here by itself.
  // 'full' (old recordings are being deleted) outranks 'nearly_full' if both arrive while a recording runs.
  alertStorage(alert) {
    if (this.state.recording) {
      if (this.state.pendingAlert !== 'full') this.state.pendingAlert = alert
      return
    }
    this.state.pendingAlert = null
    push({ url: 'page/storage', params: JSON.stringify({ state: alert }) })
  },
})
