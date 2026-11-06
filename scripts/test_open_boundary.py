import tempfile
import unittest
from pathlib import Path
from check_open_boundary import violations


class ShippedAssets(unittest.TestCase):
    def test_provider_in_embedded_console_and_installed_executable_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "console.html").write_text('<script src="https://js.stripe.com/v3/"></script>')
            (root / "update-rollback").write_text('#!/bin/sh\ncurl https://api.stripe.com/v1/charges\n')
            found = violations([root])
            self.assertEqual(len(found), 2)
            self.assertTrue(any('console.html:1:' in x for x in found))
            self.assertTrue(any('update-rollback:2:' in x for x in found))

    def test_clean_assets_and_binary_files_are_supported(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "console.html").write_text('<p>Local owner console</p>')
            (root / "update-rollback").write_text('#!/bin/sh\necho rollback\n')
            (root / "binary").write_bytes(b'\x00\xff')
            self.assertEqual(violations([root]), [])


if __name__ == '__main__':
    unittest.main()
