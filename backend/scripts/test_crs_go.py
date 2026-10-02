import hashlib
from pathlib import Path
import tempfile
import unittest

from crs_go import verify_file


class OverlayIntegrityTest(unittest.TestCase):
    def test_original_content_must_match_reviewed_hash(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "log.go"
            source.write_bytes(b"reviewed source")
            expected = hashlib.sha256(source.read_bytes()).hexdigest()
            verify_file(source, expected)
            source.write_bytes(b"changed source")
            with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                verify_file(source, expected)


if __name__ == "__main__":
    unittest.main()
