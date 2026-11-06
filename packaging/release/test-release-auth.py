#!/usr/bin/env python3
"""Exercise release credential guards without contacting GitHub or publishing."""
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = (ROOT / '.github/workflows/release.yml').read_text()


def step(name):
    return WORKFLOW.split('      - name: ' + name + '\n', 1)[1].split('      - name:', 1)[0]


def script(name):
    return textwrap.dedent(step(name).split('        run: |\n', 1)[1])


class ReleaseAuthTests(unittest.TestCase):
    def configuration(self, *, enabled='true', app='123', key='true', event='workflow_dispatch', ref='refs/heads/main'):
        return subprocess.run(['bash', '-ec', script('Require test release App configuration')],
                              env=dict(os.environ, TEST_ONLY=enabled, TEST_RELEASE_APP_ID=app,
                                       TEST_RELEASE_APP_KEY_CONFIGURED=key, GITHUB_EVENT_NAME=event,
                                       GITHUB_REF=ref), capture_output=True, text=True)

    def test_configuration_fails_before_cloud_access(self):
        for kwargs in ({'app': ''}, {'key': 'false'}, {'ref': 'refs/heads/other'}, {'event': 'push'}):
            with self.subTest(kwargs=kwargs):
                self.assertNotEqual(self.configuration(**kwargs).returncode, 0)
        self.assertEqual(self.configuration().returncode, 0)
        self.assertEqual(self.configuration(enabled='false', app='', key='false', event='push').returncode, 0)
        guard = WORKFLOW.index('      - name: Require test release App configuration')
        mint = WORKFLOW.index('      - name: Create scoped test release token')
        login = WORKFLOW.index('      - name: Azure login (test)')
        self.assertLess(guard, mint)
        self.assertLess(mint, login)

    def test_app_is_main_test_only_scoped_and_revoked(self):
        action = step('Create scoped test release token')
        for required in (
            "if: github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main' && inputs.test_only",
            'actions/create-github-app-token@64ab729a6294000f5aa4a4bc0b73d1f32bc45510',
            'repositories: ${{ github.event.repository.name }}',
            'owner: ${{ github.repository_owner }}',
            'permission-contents: write', 'permission-workflows: write',
            'skip-token-revoke: false',
        ):
            self.assertIn(required, action)
        self.assertEqual(action.count('permission-'), 2)
        self.assertNotIn('steps.test-release-token', step('Automatically release successful main'))

    def publish(self, enabled, token, *, channel='test'):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            publisher = root / 'packaging/release/publish-agent.sh'
            publisher.parent.mkdir(parents=True)
            publisher.write_text('#!/bin/sh\nprintf "%s|%s|%s" "$GH_TOKEN" "$GITHUB_SHA" "$1" > receipt\n')
            publisher.chmod(0o700)
            result = subprocess.run(['bash', '-ec', script('Publish signed agent')], cwd=root,
                                    env=dict(os.environ, TEST_ONLY=enabled, TEST_RELEASE_TOKEN=token,
                                             GH_TOKEN='builtin-fixture', RELEASE_SOURCE_SHA='a' * 40,
                                             channel=channel), capture_output=True, text=True)
            return result, (root / 'receipt').read_text() if (root / 'receipt').exists() else None

    def test_test_only_publisher_receives_app_token_and_exact_source(self):
        result, receipt = self.publish('true', 'app-fixture')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(receipt, 'app-fixture|' + 'a' * 40 + '|test')
        self.assertNotIn('app-fixture', result.stdout + result.stderr)

    def test_missing_app_token_never_falls_back_to_builtin(self):
        result, receipt = self.publish('true', '')
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(receipt)

    def test_app_token_is_refused_for_production(self):
        result, receipt = self.publish('true', 'app-fixture', channel='prod')
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(receipt)

    def test_default_and_prod_publishers_keep_builtin_token(self):
        for channel in ('test', 'prod'):
            with self.subTest(channel=channel):
                result, receipt = self.publish('false', 'unused-app-fixture', channel=channel)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(receipt, 'builtin-fixture|' + 'a' * 40 + '|' + channel)


if __name__ == '__main__':
    unittest.main()
