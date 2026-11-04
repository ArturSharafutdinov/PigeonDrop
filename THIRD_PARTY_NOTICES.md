# Third-party notices

PigeonDrop's own code and documents are licensed under the [MIT License](LICENSE). The project includes, builds on and runs the components below, each under its own license. This file is the list the licenses ask for; it is kept short on purpose, so for the full list of packages use the tools of each ecosystem (`go list -m all`, `npm ls`, `pip freeze`).

## Included in this repository

### MessageBuilder (`PigeonDrop/shared/`)

The Bluetooth messaging library of the Zepp OS sample apps, copied into the watch app and the Side Service because it is not published as a package.

- Source: <https://github.com/zepp-health/zeppos-samples>, `application/2.0/todo-list/shared/`
- License: [Apache License 2.0](PigeonDrop/shared/LICENSE)
- Copyright (c) 2021-present Zepp Health. All Rights Reserved.
- `data.js`, `defer.js` and `event.js` are unmodified. `message.js` and `message-side.js` are modified: the debug flag `DEBUG` is set to `false` (the upstream value made sending a recording crawl, see the comment in the file), `message-side.js` falls back to a no-op logger when the global `Logger` does not exist, and the comments that were written in Chinese are translated into English. Each file says so in its header.

The `NOTICE` file of the upstream repository reads:

```
Copyright (c) 2021-present Zepp Health. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

### The Zepp OS skill (`.claude/skills/zeppos/`)

Contains no copy of Zepp's documentation or samples: it downloads them on demand from <https://github.com/zepp-health/zeppos-docs> and <https://github.com/zepp-health/zeppos-samples> (both Apache-2.0, Zepp Health).

## Used at run time, not included

### Speech recognition model

The transcripts are made by **NVIDIA Parakeet TDT 0.6B v3**, in the ONNX conversion `istupakov/parakeet-tdt-0.6b-v3-onnx` (by Ilya Stupakov), loaded with **onnx-asr** (MIT) on **ONNX Runtime** (MIT).

- Model: <https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3> (NVIDIA), <https://huggingface.co/istupakov/parakeet-tdt-0.6b-v3-onnx> (conversion)
- License: [Creative Commons Attribution 4.0 International (CC BY 4.0)](https://creativecommons.org/licenses/by/4.0/)
- Changes: the model is used in its int8-quantized ONNX form; nothing else is changed. The service `stt/` downloads it while its Docker image is built, at a pinned revision, and the pairing page names the model and links the license next to every transcript.

### Go modules (`backend/go.mod`)

| Module | License |
|---|---|
| `github.com/jackc/pgx` (and `pgpassfile`, `pgservicefile`, `puddle`) | MIT |
| `github.com/pressly/goose` | MIT |
| `github.com/oapi-codegen/runtime` | Apache-2.0 |
| `github.com/google/uuid` | BSD-3-Clause |
| `github.com/tphakala/go-opus` | BSD-3-Clause (a pure-Go port of libopus: Copyright Xiph.Org, Skype Limited, Octasic, Jean-Marc Valin, Timothy B. Terriberry, CSIRO, Gregory Maxwell, Mark Borgerding, Erik de Castro Lopo, Mozilla, Amazon; Copyright Tomi P. Hakala for the Go port) |
| `github.com/tphakala/simd` | MIT |
| `github.com/sethvargo/go-retry` | Apache-2.0 |
| `github.com/mfridman/interpolate`, `github.com/apapsch/go-jsonmerge`, `go.uber.org/multierr` | MIT |
| `golang.org/x/crypto`, `x/term`, `x/text`, `x/sync`, `x/sys` | BSD-3-Clause |

The code generators (`oapi-codegen`, Apache-2.0, and `sqlc`, MIT) are build tools in `backend/tools` and are not part of what is shipped.

### Python packages (`stt/requirements.txt`)

`onnx-asr` (MIT), `onnxruntime` (MIT), `numpy` (BSD-3-Clause), `huggingface_hub` (Apache-2.0) and their dependencies, each under its own permissive license. The exact versions are pinned in `stt/requirements.txt`.

### Watch app tooling

`@zeppos/device-types` (ISC), type declarations used while developing; nothing is bundled into the app.

### Container images and services

The Docker images are built `FROM` the official `golang` (BSD-3-Clause), `gcr.io/distroless/static` (Apache-2.0) and `python` (PSF) images. The server stores its data in PostgreSQL (PostgreSQL License).

## Trademarks

Amazfit, Zepp and Zepp OS are trademarks of Zepp Health and its affiliates; Telegram is a trademark of Telegram FZ-LLC. PigeonDrop is an independent project, is not affiliated with or endorsed by either company, and uses the names only to say what it works with.
