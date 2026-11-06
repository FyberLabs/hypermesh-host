#!/usr/bin/env python3
"""Exercise workflow source selection with real isolated Git histories."""
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT=Path(__file__).resolve().parents[2]
workflow=(ROOT/'.github/workflows/release.yml').read_text()
section=workflow.split('      - name: Select approved test source\n',1)[1].split('      - name:',1)[0]
SCRIPT=textwrap.dedent(section.split('        run: |\n',1)[1])

class SourceSelectionTests(unittest.TestCase):
    def run_case(self, selection, *, enabled='true', ref='refs/heads/main'):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            def git(*args):return subprocess.check_output(['git',*args],cwd=root,stderr=subprocess.DEVNULL,text=True).strip()
            git('init','-b','main');git('config','user.email','fixture@example.invalid');git('config','user.name','Fixture')
            (root/'file').write_text('approved');git('add','file');git('commit','-m','approved');approved=git('rev-parse','HEAD')
            git('checkout','-b','unmerged');(root/'file').write_text('unmerged');git('commit','-am','unmerged');unmerged=git('rev-parse','HEAD')
            git('checkout','main');(root/'file').write_text('newer main');git('commit','-am','newer main');main=git('rev-parse','HEAD')
            expected={'approved':approved,'unmerged':unmerged,'malformed':'main'}[selection]
            output=root/'env'
            result=subprocess.run(['bash','-c',SCRIPT],cwd=root,env=dict(os.environ,HYPERMESH_TEST_ONLY=enabled,EXPECTED_TEST_SOURCE_SHA=expected,GITHUB_SHA=main,GITHUB_REF=ref,GITHUB_EVENT_NAME='workflow_dispatch',GITHUB_ENV=str(output)),capture_output=True,text=True)
            return result.returncode,git('rev-parse','HEAD'),approved,main,output.read_text() if output.exists() else ''

    def test_approved_historical_main_is_selected(self):
        code,head,approved,_,output=self.run_case('approved')
        self.assertEqual(code,0);self.assertEqual(head,approved);self.assertEqual(output,'RELEASE_SOURCE_SHA='+approved+'\n')

    def test_unmerged_and_malformed_sources_are_refused(self):
        for selection in ('unmerged','malformed'):
            with self.subTest(selection=selection):
                code,head,_,main,output=self.run_case(selection)
                self.assertNotEqual(code,0);self.assertEqual(head,main);self.assertEqual(output,'')

    def test_other_ref_is_refused(self):
        code,head,_,main,output=self.run_case('approved',ref='refs/heads/other')
        self.assertNotEqual(code,0);self.assertEqual(head,main);self.assertEqual(output,'')

    def test_cleanup_survives_source_without_helper(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); checkout=root/'checkout'; checkout.mkdir()
            runner_temp=root/'temp'; runner_temp.mkdir()
            def git(*args):return subprocess.check_output(['git',*args],cwd=checkout,stderr=subprocess.DEVNULL,text=True).strip()
            git('init','-b','main');git('config','user.email','fixture@example.invalid');git('config','user.name','Fixture')
            (checkout/'file').write_text('historical source');git('add','.');git('commit','-m','historical');approved=git('rev-parse','HEAD')
            helper=checkout/'packaging/release/cleanup.sh';helper.parent.mkdir(parents=True)
            helper.write_text((ROOT/'packaging/release/cleanup.sh').read_text());git('add','.');git('commit','-m','cleanup fix');main=git('rev-parse','HEAD')
            env=dict(os.environ,RUNNER_TEMP=str(runner_temp),HYPERMESH_TEST_ONLY='true',EXPECTED_TEST_SOURCE_SHA=approved,GITHUB_SHA=main,GITHUB_REF='refs/heads/main',GITHUB_EVENT_NAME='workflow_dispatch',GITHUB_ENV=str(root/'env'))
            preserve=workflow.split('      - name: Preserve workflow cleanup\n',1)[1].split('      - name:',1)[0].split('        run: ',1)[1].strip()
            subprocess.run(['bash','-ec',preserve],cwd=checkout,env=env,check=True)
            subprocess.run(['bash','-ec',SCRIPT],cwd=checkout,env=env,check=True,capture_output=True)
            self.assertFalse(helper.exists())
            azure=runner_temp/'az';azure.mkdir();(azure/'fixture-cache').write_text('disposable fixture')
            wipe=workflow.split('      - name: Wipe signing material and log out\n',1)[1].split('      - name:',1)[0].split('        run: ',1)[1].strip()
            subprocess.run(['bash','-ec',wipe],cwd=checkout,env=dict(env,AZURE_CONFIG_DIR=str(azure),GNUPGHOME=''),check=True)
            self.assertFalse(azure.exists())

    def test_default_publication_keeps_event_source(self):
        code,head,_,main,output=self.run_case('approved',enabled='false')
        self.assertEqual(code,0);self.assertEqual(head,main);self.assertEqual(output,'RELEASE_SOURCE_SHA='+main+'\n')

if __name__=='__main__':unittest.main()
