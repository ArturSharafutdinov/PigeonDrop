import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { onKey, KEY_SHORTCUT, KEY_EVENT_CLICK, KEY_EVENT_LONG_PRESS } from '@zos/interaction'
import { create, id, codec } from '@zos/media'
import { push } from '@zos/router'
import TransferFile from '@zos/ble/TransferFile'
import { newRecordingPath, listRecordings, pruneOldRecordings } from '../utils/recordings'

Page({
  state: {
    recording: false,
    currentFile: null,
    recorder: null,
    player: null,
    statusText: null,
  },

  build() {
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

  stopRecording() {
    this.state.recorder.stop()
    this.state.recorder = null
    this.state.recording = false
    pruneOldRecordings()
    this.updateStatus('Отправка...')
    this.sendRecording(this.state.currentFile)
  },

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

  playLatestRecording() {
    if (this.state.recording) return

    const recordings = listRecordings()
    if (recordings.length === 0) {
      this.updateStatus('Нет записей для\nвоспроизведения')
      return
    }

    this.playFile(recordings[0].filePath)
  },

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
