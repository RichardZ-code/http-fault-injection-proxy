import importlib.util
from pathlib import Path
import shutil
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('evidence', Path(__file__).with_name('evidence.py'))
evidence = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evidence)
SOURCE = Path(__file__).resolve().parent.parent / 'benchmarks/results/2026-10-06-macos-arm64-0da4d1f43560'


class PublishedEvidence(unittest.TestCase):
    def test_approved_subset(self):
        self.assertEqual(evidence.verify(SOURCE), 170)

    def test_mutation_and_extra_file_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'subset'
            shutil.copytree(SOURCE, root)
            raw = root / 'dataset/correctness/requests.json'
            raw.write_text('[]')
            with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
                evidence.verify(root)
            shutil.copyfile(SOURCE / 'dataset/correctness/requests.json', raw)
            (root / 'extra').write_text('unreviewed')
            with self.assertRaisesRegex(ValueError, 'inventory mismatch'):
                evidence.verify(root)


if __name__ == '__main__':
    unittest.main()
