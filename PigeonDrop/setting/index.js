// The settings of PigeonDrop in the Zepp app on the phone (the Zepp OS "Settings App"): the address and the access
// token of a server the person runs themselves ("Personal Server"). The values stay in the Zepp app's
// `settingsStorage`; nothing is sent anywhere from here. The watch asks the phone for them when the person presses
// Settings > Connection > Personal Server (app-side/serverConfig.js) and asks the person to confirm them on its screen.
//
// The text is English only for now: how this screen could follow the language of the phone or the watch has not been
// looked into. The checks of the typed values are in ./problems.js.

import { addressProblem, tokenProblem, isReady } from './problems'

const HINT = { fontSize: '13px', color: '#666666', marginBottom: '14px' }
const PROBLEM = { fontSize: '13px', color: '#d93025', marginBottom: '14px' }

AppSettingsPage({
  build(props) {
    const read = (key) => props.settingsStorage.getItem(key) || ''
    const address = read('serverUrl')
    const token = read('serverToken')

    const children = [
      Text({ bold: true, style: { fontSize: '20px', marginBottom: '8px' } }, 'Personal Server'),
      Text(
        { paragraph: true, style: HINT },
        'Send your recordings to a server you run yourself instead of PigeonDrop. Enter its address and an access token. ' +
          'Use a token made just for the watch, one that can only add recordings and can be revoked.',
      ),
      TextInput({ label: 'Server address', placeholder: 'https://my-server.example', settingsKey: 'serverUrl' }),
    ]
    const addressText = addressProblem(address)
    if (addressText) children.push(Text({ paragraph: true, style: PROBLEM }, addressText))

    children.push(
      TextInput({ label: 'Access token', settingsKey: 'serverToken' }),
      Text({ paragraph: true, style: HINT }, 'The token is shown while you type it. It stays in the Zepp app on this phone until the watch asks for it.'),
    )
    const tokenText = tokenProblem(token)
    if (tokenText) children.push(Text({ paragraph: true, style: PROBLEM }, tokenText))

    children.push(
      Text(
        { paragraph: true, style: { fontSize: '14px', marginTop: '8px' } },
        isReady(address, token)
          ? 'Done here. Now on the watch open Settings, Connection, Personal Server and confirm the address.'
          : 'When both fields are filled in, open Settings, Connection, Personal Server on the watch.',
      ),
    )

    return View({ style: { padding: '16px 20px' } }, children)
  },
})
