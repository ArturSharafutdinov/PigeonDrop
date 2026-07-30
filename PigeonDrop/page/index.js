// Main screen of the app. Lets the user record a voice message with the
// watch's hardware "shortcut" button, sends every finished recording to the
// paired phone, and gives quick access to the full recordings list.

import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { onKey, KEY_SHORTCUT, KEY_EVENT_CLICK, KEY_EVENT_LONG_PRESS } from '@zos/interaction'
import { create, id, codec } from '@zos/media'
import { push } from '@zos/router'
import { readFileSync } from '@zos/fs'
import TransferFile from '@zos/ble/TransferFile'
import { newRecordingPath, listRecordings, pruneOldRecordings } from '../utils/recordings'

Page({
  state: {
    recording: false, // true while the Recorder is actively capturing audio
    currentFile: null, // data:// path of the recording currently in progress
    recorder: null, // active Recorder instance, or null when idle
    player: null, // lazily created Player instance, reused across plays (see getPlayer)
    statusText: null, // reference to the on-screen status TEXT widget, so we can update it later
  },

  build() {
    // Big status label in the upper half of the screen. Height is capped at
    // 230px (not the full 480px screen) to leave room for the button below -
    // see the "list" button's placement note for why.
    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(70),
      w: px(480),
      h: px(230),
      text: this.idleText(),
      text_size: px(28),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
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

  // Stops the current recording, enforces the storage limit (config.js),
  // and immediately kicks off sending the file to the phone.
  stopRecording() {
    this.state.recorder.stop()
    this.state.recorder = null
    this.state.recording = false
    pruneOldRecordings()
    this.updateStatus('Отправка...')
    this.sendRecording(this.state.currentFile)
    this.sendRecordingViaMessage(this.state.currentFile)
  },

  // Hands the recording off to the paired phone over BLE. The phone-side
  // counterpart (app-side/index.js) is what actually receives it - this
  // only tracks whether the transfer itself succeeded from the watch's
  // point of view.
  sendRecording(filePath) {
    const transferFile = new TransferFile()
    const outbox = transferFile.getOutbox()
    const fileObject = outbox.enqueueFile(filePath)
    this.state.fileObject = fileObject

    fileObject.on('change', (event) => {
      if (event.data.readyState === 'transferred') {
        this.updateStatus('Отправлено!\n\n' + this.idleText())
      } else if (event.data.readyState === 'error') {
        this.updateStatus('Ошибка отправки\n\n' + this.idleText())
      }
    })
  },

  // Sends the recording to the Side Service over MessageBuilder instead of
  // TransferFile - TransferFile can't be read back on the phone (see the
  // research spike notes in shared/message.js), so this is the path that
  // actually reaches app-side/index.js's server upload. Runs alongside
  // sendRecording() for now - not a replacement.
  //
  // Encoding to base64 here (not on the phone) means the payload sent over
  // BLE is what gets JSON.stringify'd server-side too - one encoding step
  // instead of two, and the Side Service can forward `audio` to fetch()
  // without touching it.
  sendRecordingViaMessage(filePath) {
    const relativePath = filePath.replace('data://', '')
    const arrayBuffer = readFileSync({ path: relativePath })

    if (!arrayBuffer) {
      console.log('sendRecordingViaMessage: failed to read file', relativePath)
      return
    }

    const fileName = relativePath.split('/').pop()
    const audio = Buffer.from(arrayBuffer).toString('base64')

    const { messageBuilder } = getApp()._options.globalData
    messageBuilder.call({ fileName, audio })
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
