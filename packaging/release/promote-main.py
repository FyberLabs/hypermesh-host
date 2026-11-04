#!/usr/bin/env python3
"""After a successful main publish, queue its existing signed prod publisher.
GITHUB_TOKEN-created tags do not trigger push workflows, so dispatch explicitly.
The release-publish concurrency lock serializes tag allocation with publishers.
"""
import json
import os
import re
import subprocess
import time

REPO = 'FyberLabs/hypermesh-host'
STABLE = re.compile(r'v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)')

def choose_tag(tags):
    stable = [(tuple(map(int, STABLE.fullmatch(tag).groups())), tag, commit)
              for tag, commit in tags.items() if STABLE.fullmatch(tag)]
    # A main rerun has published a new test build. A partially published prod
    # tag may now be below that global floor, so allocate a fresh patch even
    # for the same commit. Tag-workflow reruns retain existing immutable reuse.
    highest = max(stable)[0] if stable else (0, 1, -1)
    return 'v%d.%d.%d' % (highest[0], highest[1], highest[2] + 1), True

def run(*args):
    return subprocess.check_output(args, text=True).strip()

def api(path, value=None):
    args = ['gh', 'api', f'repos/{REPO}/{path}']
    if value is None:
        return json.loads(run(*args))
    return json.loads(subprocess.check_output(args + ['--method','POST','--input','-'], input=json.dumps(value),text=True))

def wait_for_ci(revision):
    deadline = time.monotonic() + 35 * 60
    while time.monotonic() < deadline:
        runs = api(f'actions/workflows/ci.yml/runs?head_sha={revision}&event=push&branch=main&per_page=100')['workflow_runs']
        exact = [r for r in runs if r['head_sha'] == revision and r['head_branch'] == 'main' and r['event'] == 'push']
        if exact:
            latest = max(exact, key=lambda r: r['id'])
            if latest['status'] == 'completed':
                if latest['conclusion'] != 'success':
                    raise ValueError('main CI failed; production was not dispatched')
                return
        time.sleep(15)
    raise TimeoutError('main CI did not succeed before the promotion deadline')

def promote(env):
    revision = env.get('GITHUB_SHA','')
    if env.get('GITHUB_REPOSITORY') != REPO or env.get('GITHUB_REF') != 'refs/heads/main' or not re.fullmatch('[0-9a-f]{40}',revision):
        raise ValueError('automatic release requires this repository main commit')
    if api('commits/main')['sha'] != revision:
        print('A newer main revision supersedes this queued publish; it will release automatically.')
        return
    wait_for_ci(revision)
    if api('commits/main')['sha'] != revision:
        print('Main advanced while CI ran; its newer revision will release.')
        return
    refs = json.loads(run('gh','api','--paginate','--slurp',f'repos/{REPO}/git/matching-refs/tags/v'))
    tags = {}
    for page in refs:
        for ref in page:
            tag = ref['ref'].removeprefix('refs/tags/')
            if not STABLE.fullmatch(tag):
                continue
            # Dereference annotated tags through the commit endpoint as well.
            tags[tag] = api(f'commits/{tag}')['sha']
    tag, create = choose_tag(tags)
    if create:
        api('git/refs',dict(ref=f'refs/tags/{tag}',sha=revision))
    # workflow_dispatch is deliberately triggered with the built-in token;
    # no PAT/App, reviewer, or main-to-prod Azure permission expansion.
    run('gh','workflow','run','release.yml','--repo',REPO,'--ref',tag)
    print(f'Queued signed production release {tag} for {revision}.')

if __name__ == '__main__':
    promote(os.environ)
