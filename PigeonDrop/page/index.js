// Main screen of the app. Lets the user record a voice message with the
// watch's hardware "shortcut" button, sends every finished recording to the
// paired phone, and gives quick access to the full recordings list.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { onKey, KEY_SHORTCUT, KEY_EVENT_CLICK, KEY_EVENT_LONG_PRESS } from '@zos/interaction'
import { create, id, codec } from '@zos/media'
import { push } from '@zos/router'
import { newRecordingPath, listRecordings } from '../utils/recordings'
import { pruneRecordings, retryPendingUploads, isUploaded } from '../utils/uploadQueue'

Page({
  state: {
    recording: false, // true while the Recorder is actively capturing audio
    currentFile: null, // data:// path of the recording currently in progress
    recorder: null, // active Recorder instance, or null when idle
    player: null, // lazily created Player instance, reused across plays (see getPlayer)
    statusText: null, // reference to the on-screen status TEXT widget, so we can update it later
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

    // Navigates to the server-configuration page (QR code + sync - see
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

    // App-open retry trigger for the upload queue - catches anything that
    // was still pending from a previous session (e.g. the watch was out of
    // BLE range or the server was down when it was recorded).
    retryPendingUploads()
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
    const filePath = newRecordingPath()
    const recorder = create(id.RECORDER)
    recorder.setFormat(codec.OPUS, { target_file: filePath })
    recorder.start()

    this.state.recorder = recorder
    this.state.currentFile = filePath
    this.state.recording = true
    this.updateStatus('Идёт запись...\nНажмите ещё раз\nчтобы остановить')
  },

  // Stops the current recording, enforces the storage caps (config.js), and
  // kicks off a retry sweep of the upload queue - which now includes this
  // recording as well as any older ones still waiting (utils/uploadQueue.js).
  stopRecording() {
    this.state.recorder.stop()
    this.state.recorder = null
    this.state.recording = false
    pruneRecordings()
    retryPendingUploads()

    // Optimistic status - MessageBuilder#call is fire-and-forget, so this
    // doesn't wait for the phone to actually finish uploading. The real
    // upload result (success/failure, from the server) comes back later via
    // app.js's messageBuilder.on('call', ...) listener, which is also what
    // marks a recording as uploaded - so pendingCount here reflects whatever
    // was still true just before this sweep kicked off.
    const pendingCount = listRecordings().filter((item) => !isUploaded(item.fileName)).length
    const status = pendingCount > 0 ? 'Отправка...\nВ очереди: ' + pendingCount : 'Отправлено\n\n' + this.idleText()
    this.updateStatus(status)
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
})
