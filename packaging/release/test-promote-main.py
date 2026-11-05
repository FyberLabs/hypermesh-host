import importlib.util
from pathlib import Path
import unittest
import subprocess
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('promote',Path(__file__).with_name('promote-main.py'))
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)
class PromotionTests(unittest.TestCase):
    def test_test_only_publication_binds_the_approved_main_source(self):
        revision='a'*40
        for flag,ref,expected,ok in [
            ('true','refs/heads/main',revision,True),
            ('true','refs/heads/main','b'*40,False),
            ('true','refs/heads/main','',False),
            ('true','refs/tags/v0.1.0',revision,False),
            ('true','refs/heads/feature',revision,False),
            ('false','refs/tags/v0.1.0','',True),
        ]:
            with self.subTest(flag=flag,ref=ref,expected=expected):
                result=subprocess.run(['bash','-c','source "$1"; shift; require_test_only_source "$@"','test',str(Path(__file__).with_name('layout.sh')),flag,ref,revision,expected],capture_output=True)
                self.assertEqual(result.returncode==0,ok)
    def test_manual_test_only_never_queries_tags_or_dispatches_production(self):
        env=dict(GITHUB_EVENT_NAME='workflow_dispatch',GITHUB_REF='refs/heads/main',HYPERMESH_TEST_ONLY='true')
        with patch.object(p,'api') as api,patch.object(p,'run') as run,patch.object(p,'wait_for_ci') as wait:
            p.promote(env)
        api.assert_not_called();run.assert_not_called();wait.assert_not_called()
    def test_test_only_cannot_select_a_tag_or_push(self):
        for event,ref in [('push','refs/heads/main'),('workflow_dispatch','refs/tags/v0.1.0')]:
            with patch.object(p,'api') as api,self.assertRaises(ValueError):
                p.promote(dict(GITHUB_EVENT_NAME=event,GITHUB_REF=ref,HYPERMESH_TEST_ONLY='true'))
            api.assert_not_called()
    def test_main_retry_allocates_fresh_patch_after_new_test_build(self):
        self.assertEqual(p.choose_tag({}),('v0.1.0',True))
        self.assertEqual(p.choose_tag({'v1.2.9':'a','v1.2.10':'b','v9.0.0-rc.1':'c'}),('v1.2.11',True))
        self.assertEqual(p.choose_tag({'v1.2.9':'a','v1.2.10':'a'}),('v1.2.11',True))
    def test_other_branches_cannot_create_release(self):
        with patch.object(p,'api') as api, self.assertRaises(ValueError):
            p.promote(dict(GITHUB_REPOSITORY=p.REPO,GITHUB_REF='refs/heads/feature',GITHUB_SHA='a'*40))
        api.assert_not_called()
    def test_superseded_main_does_not_dispatch_old_code(self):
        with patch.object(p,'api',return_value={'sha':'b'*40}),patch.object(p,'run') as run:
            p.promote(dict(GITHUB_REPOSITORY=p.REPO,GITHUB_REF='refs/heads/main',GITHUB_SHA='a'*40))
        run.assert_not_called()
    def test_tag_creation_failure_never_dispatches(self):
        env=dict(GITHUB_REPOSITORY=p.REPO,GITHUB_REF='refs/heads/main',GITHUB_SHA='a'*40)
        with patch.object(p,'api',side_effect=[{'sha':'a'*40},{'sha':'a'*40},RuntimeError('tag already exists')]),patch.object(p,'wait_for_ci'),patch.object(p,'run',return_value='[[]]') as run:
            with self.assertRaises(RuntimeError):p.promote(env)
        self.assertEqual(run.call_count,1)
    def test_current_green_main_creates_tag_and_dispatches_tag_workflow(self):
        env=dict(GITHUB_REPOSITORY=p.REPO,GITHUB_REF='refs/heads/main',GITHUB_SHA='a'*40)
        with patch.object(p,'api',side_effect=[{'sha':'a'*40},{'sha':'a'*40},{}]) as api, patch.object(p,'wait_for_ci'), patch.object(p,'run',side_effect=['[[]]','']) as run:
            p.promote(env)
        self.assertEqual(api.call_args.args,('git/refs',dict(ref='refs/tags/v0.1.0',sha='a'*40)))
        self.assertEqual(run.call_args.args,('gh','workflow','run','release.yml','--repo',p.REPO,'--ref','v0.1.0'))
    def test_failed_main_ci_cannot_promote(self):
        run=dict(id=1,head_sha='a'*40,head_branch='main',event='push',status='completed',conclusion='failure')
        with patch.object(p,'api',return_value=dict(workflow_runs=[run])),self.assertRaises(ValueError):
            p.wait_for_ci('a'*40)
    def test_successful_main_ci_is_accepted(self):
        run=dict(id=1,head_sha='a'*40,head_branch='main',event='push',status='completed',conclusion='success')
        with patch.object(p,'api',return_value=dict(workflow_runs=[run])):
            p.wait_for_ci('a'*40)
if __name__=='__main__' :unittest.main()
