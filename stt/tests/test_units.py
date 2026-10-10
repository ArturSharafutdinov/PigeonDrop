"""The small pieces: WAV reading, settings and the count of usable cores."""
import unittest

from app import config as configuration
from app import cpus, wavio

from .helpers import wav


class WavTest(unittest.TestCase):
    def test_reads_the_samples_and_the_length(self):
        audio = wavio.read_wav(wav(1.5), max_seconds=10)
        self.assertEqual(len(audio.pcm), 24000 * 2)
        self.assertEqual((audio.seconds, audio.milliseconds), (1.5, 1500))

    def test_the_limit_is_inclusive(self):
        wavio.read_wav(wav(2.0), max_seconds=2.0)
        with self.assertRaises(wavio.TooLong):
            wavio.read_wav(wav(frames=32001), max_seconds=2.0)

    def test_every_other_shape_is_refused_with_a_reason(self):
        cases = {
            "garbage": (b"RIFFxxxxWAVEnothing", "not a PCM WAV"),
            "stereo": (wav(channels=2), "mono"),
            "rate": (wav(rate=22050), "16000 Hz"),
            "width": (wav(width=1), "16-bit"),
            "empty": (wav(frames=0), "no audio"),
            "cut": (wav(1.0, header_frames=40000), "cut short"),
        }
        for label, (data, reason) in cases.items():
            with self.subTest(label):
                with self.assertRaises(wavio.BadAudio) as caught:
                    wavio.read_wav(data, max_seconds=10)
                self.assertIn(reason, str(caught.exception))


class ConfigTest(unittest.TestCase):
    def test_defaults(self):
        config = configuration.load({"STT_TOKEN": "t"})
        self.assertEqual((config.port, config.host, config.max_audio_seconds, config.threads, config.log_level),
                         (8000, "0.0.0.0", 65.0, None, "info"))
        self.assertFalse(config.insecure)

    def test_the_body_limit_follows_the_audio_limit(self):
        config = configuration.load({"STT_TOKEN": "t", "MAX_AUDIO_SECONDS": "30"})
        self.assertEqual(config.max_body_bytes, 30 * 32000 + 4096)

    def test_every_problem_is_reported_at_once(self):
        bad = {"STT_INSECURE": "maybe", "PORT": "0", "MAX_AUDIO_SECONDS": "-1", "STT_THREADS": "many", "LOG_LEVEL": "loud"}
        with self.assertRaises(configuration.ConfigError) as caught:
            configuration.load(bad)
        message = str(caught.exception)
        for name in ("STT_INSECURE", "PORT", "MAX_AUDIO_SECONDS", "STT_THREADS", "LOG_LEVEL"):
            self.assertIn(name, message)

    def test_a_missing_token_is_an_error_unless_insecure(self):
        with self.assertRaises(configuration.ConfigError):
            configuration.load({})
        self.assertTrue(configuration.load({"STT_INSECURE": "1"}).insecure)

    def test_limits_of_the_numbers(self):
        for name, value in (("PORT", "65536"), ("MAX_AUDIO_SECONDS", "601"), ("MAX_AUDIO_SECONDS", "0.5"),
                            ("STT_THREADS", "65"), ("STT_THREADS", "0")):
            with self.subTest(name + "=" + value):
                with self.assertRaises(configuration.ConfigError):
                    configuration.load({"STT_TOKEN": "t", name: value})


class CpusTest(unittest.TestCase):
    def test_cgroup_v2_quota(self):
        self.assertIsNone(cpus.quota_cpus_v2("max 100000"))
        self.assertIsNone(cpus.quota_cpus_v2(None))
        self.assertIsNone(cpus.quota_cpus_v2("garbage"))
        self.assertIsNone(cpus.quota_cpus_v2("100000 0"))
        self.assertEqual(cpus.quota_cpus_v2("100000 100000"), 1)
        self.assertEqual(cpus.quota_cpus_v2("200000 100000"), 2)
        self.assertEqual(cpus.quota_cpus_v2("150000 100000"), 2)  # a part of a core still needs a thread
        self.assertEqual(cpus.quota_cpus_v2("50000 100000"), 1)

    def test_cgroup_v1_quota(self):
        self.assertIsNone(cpus.quota_cpus_v1("-1\n", "100000\n"))
        self.assertIsNone(cpus.quota_cpus_v1(None, None))
        self.assertEqual(cpus.quota_cpus_v1("200000\n", "100000\n"), 2)

    def test_the_smaller_of_the_cores_it_may_run_on_and_the_quota(self):
        self.assertEqual(cpus.available_cpus(affinity=8, v2="200000 100000", v1=None), 2)  # Coolify "CPU limit 2" on a 8-core host
        self.assertEqual(cpus.available_cpus(affinity=1, v2="400000 100000", v1=None), 1)  # pinned to one core
        self.assertEqual(cpus.available_cpus(affinity=4, v2="max 100000", v1=None), 4)  # no quota
        self.assertEqual(cpus.available_cpus(affinity=4, v2=None, v1=("100000", "100000")), 1)  # cgroup v1
        self.assertEqual(cpus.available_cpus(affinity=0, v2=None, v1=("-1", "100000")), 1)  # never below one


if __name__ == "__main__":
    unittest.main()
