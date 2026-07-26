import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { onKey, KEY_SHORTCUT, KEY_EVENT_CLICK, KEY_EVENT_LONG_PRESS } from '@zos/interaction'
import { create, id, codec } from '@zos/media'
import { statSync } from '@zos/fs'
import TransferFile from '@zos/ble/TransferFile'

const RECORD_FILE_NAME = 'voice.opus'
const RECORD_FILE = `data://${RECORD_FILE_NAME}`

Page({
  state: {
    recording: false,
    hasRecording: false,
    recorder: null,
    player: null,
    statusText: null,
  },

  build() {
    this.state.hasRecording = !!statSync({ path: RECORD_FILE_NAME })

    this.state.statusText = createWidget(widget.TEXT, {
      x: px(0),
      y: px(0),
      w: px(480),
      h: px(480),
      text: this.idleText(),
      text_size: px(30),
      color: 0xffffff,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    onKey({
      callback: (key, keyEvent) => {
        if (key !== KEY_SHORTCUT) return false

        if (keyEvent === KEY_EVENT_CLICK) {
          this.toggleRecording()
          return true
        }

        if (keyEvent === KEY_EVENT_LONG_PRESS) {
          this.playRecording()
          return true
        }

        return false
      },
    })
  },

  idleText() {
    return this.state.hasRecording
      ? 'Клик — записать\nДолгое нажатие —\nвоспроизвести'
      : 'Нажмите кнопку\nчтобы начать запись'
  },

  toggleRecording() {
    if (this.state.recording) {
      this.stopRecording()
    } else {
      this.startRecording()
    }
  },

  startRecording() {
    const recorder = create(id.RECORDER)
    recorder.setFormat(codec.OPUS, { target_file: RECORD_FILE })
    recorder.start()

    this.state.recorder = recorder
    this.state.recording = true
    this.updateStatus('Идёт запись...\nНажмите ещё раз\nчтобы остановить')
  },

  stopRecording() {
    this.state.recorder.stop()
    this.state.recorder = null
    this.state.recording = false
    this.state.hasRecording = true
    this.updateStatus('Отправка...')
    this.sendRecording()
  },

  sendRecording() {
    const transferFile = new TransferFile()
    const outbox = transferFile.getOutbox()
    const fileObject = outbox.enqueueFile(RECORD_FILE)
    this.state.fileObject = fileObject

    fileObject.on('change', (event) => {
      if (event.data.readyState === 'transferred') {
        this.updateStatus('Отправлено!\n\n' + this.idleText())
      } else if (event.data.readyState === 'error') {
        this.updateStatus('Ошибка отправки\n\n' + this.idleText())
      }
    })
  },

  playRecording() {
    if (this.state.recording || !this.state.hasRecording) {
      return
    }

    const player = create(id.PLAYER)
    this.state.player = player

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

    this.updateStatus('Воспроизведение...')
    player.setSource(player.source.FILE, { file: RECORD_FILE })
    player.prepare()
  },

  updateStatus(text) {
    this.state.statusText.setProperty(prop.MORE, { text })
  },
})
