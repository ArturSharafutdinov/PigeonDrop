#!/usr/bin/env bash
# smoke.sh on recordings made up on the spot (two seconds of silence, two of a tone): for CI, which has none of the
# owner's real recordings. It shows that the image starts with the production limits, loads the model and answers
# through the real protocol; what the model makes of real speech is for smoke.sh with real recordings.
#
#   IMAGE=pigeondrop-stt:ci ./smoke-synthetic.sh
set -euo pipefail
cd "$(dirname "$0")"

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
python3 - "$dir" <<'PY'
import math
import struct
import sys
import wave

RATE = 16000


def write(name, samples):
    with wave.open(f"{sys.argv[1]}/{name}.wav", "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(b"".join(struct.pack("<h", s) for s in samples))


write("silence", [0] * (2 * RATE))
write("tone", [int(8000 * math.sin(2 * math.pi * 440 * i / RATE)) for i in range(2 * RATE)])
PY
./smoke.sh "$dir"
