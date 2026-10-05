"""Тесты сопоставления файлов и сценария (ffmpeg не нужен)."""
import os
import tempfile
import unittest

import import_recordings as importer


def touch(folder, name, mtime):
    path = os.path.join(folder, name)
    open(path, "wb").close()
    os.utime(path, (mtime, mtime))
    return path


class ListFiles(unittest.TestCase):
    def test_by_modification_time_then_name(self):
        with tempfile.TemporaryDirectory() as d:
            b = touch(d, "b.opus", 100)
            a = touch(d, "a.opus", 300)
            c = touch(d, "c.ogg", 200)
            touch(d, "notes.txt", 50)
            touch(d, ".hidden.opus", 10)
            self.assertEqual(importer.list_files(d, "mtime"), [b, c, a])
            self.assertEqual(importer.list_files(d, "name"), [a, b, c])


class Plan(unittest.TestCase):
    def test_pairs_in_order(self):
        self.assertEqual(importer.plan(["x", "y"], ["ru01", "ru02"]), [("x", "ru01"), ("y", "ru02")])

    def test_count_mismatch_is_an_error_that_explains_itself(self):
        with self.assertRaises(importer.MappingError) as ctx:
            importer.plan(["x"], ["ru01", "ru02"])
        self.assertIn("--ids", str(ctx.exception))


class CheckDuration(unittest.TestCase):
    def test_plausible(self):
        self.assertIsNone(importer.check_duration("ru01", 10, 4.2))

    def test_far_too_short_or_too_long(self):
        self.assertIn("порядок", importer.check_duration("ru01", 10, 0.8))
        self.assertIn("порядок", importer.check_duration("ru01", 10, 20.0))

    def test_silence_needs_some_length(self):
        self.assertIsNotNone(importer.check_duration("sil01", 0, 1.0))
        self.assertIsNone(importer.check_duration("sil01", 0, 9.5))


if __name__ == "__main__":
    unittest.main()
