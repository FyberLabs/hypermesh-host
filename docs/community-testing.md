# Help test a Hypermesh host

You can help bring up hardware that Fyber does not own. Download your platform's
individual signed executable from the [test channel](https://github.com/FyberLabs/hypermesh-host/releases/tag/test).
Test builds are available before hardware certification. A published binary
means the OS/architecture builds; it does not mean every attached accelerator
has an inference adapter yet.

## Request support and run a build

1. Open a [hardware support request](https://github.com/FyberLabs/hypermesh-host/issues/new?template=hardware-support.yml)
   with the exact board, CPU, RAM, accelerator devices, OS and driver/SDK versions.
   Include NVIDIA, AMD and Intel rigs, Apple silicon, ARM boards, and AI/M.2
   add-ons even when they have no current adapter. You do not need a Fyber receipt.
2. Choose Linux ARM64, Linux AMD64 or Apple silicon macOS ARM64 from the signed
   test release. Use the release's `python3 ./bootstrap.py --channel test` verifier;
   it downloads and verifies only the executable for the selected platform.
   See [platform downloads](platform-releases.md#download-from-the-test-channel).
3. Keep the key and receipt with the resulting `hypermesh-host` executable.
   Run these without sudo first:

   ```sh
   ./hypermesh-host platforms > platforms.json
   ./hypermesh-host inspect > inspect.json
   ```

   Inspect is discovery, not certification. Review the files locally. The
   hardware fingerprint identifies class characteristics, not ownership.
4. Follow the reviewed setup policy for that board/OS. Linux setup is a separate
   `sudo ./hypermesh-host` command with your approval. Mac candidates currently
   support download and inspection; native service setup is pending. Vendor
   Yocto boards require their own installation policy. Report a missing adapter
   or unsupported stage; do not substitute a different backend to claim a pass.
5. File a [test result](https://github.com/FyberLabs/hypermesh-host/issues/new?template=test-channel-feedback.yml).
   Link the hardware request and exact immutable test release. Successful,
   unsuccessful and partial results all help. A maintainer links a fix PR or
   the specific missing driver/runtime/model contract, publishes the next test
   build through the same channel, and asks for a repeat on that configuration.

A separate executable per board is usually unnecessary. The release target is
OS/architecture; device support comes from the reviewed setup profile, pinned
runtime and model/device adapter. Driver installers and model assets have their
own pins and licenses. They are not arbitrary additions to a binary archive.

## Bronze certification and test purchases

Once hyperme.sh and the required account, enrollment and certification services
are live, the first lab and volunteer nodes enter the Bronze validation track.
Use the existing owner enrollment and authenticated certification flow; keep
its reviewed runtime/model/context pins. A report is evidence for review, not a
certificate. Hardware ownership does not select a stronger certification tier.
See [authenticated certification](authenticated-certification.md) for the
implemented commands and their prerequisites.

A test purchase should exercise the normal renter flow: discover/select the
specific test host and model, reserve, check out, receive the lease, run actual
inference through Hypermesh, inspect usage and the charge receipt, stop, and
verify release/cleanup. Record denied-start, expired reservation, cancellation
and refund outcomes too. The test environment must identify whether the money
is test credits, provider sandbox funds, or an explicitly authorized real-money
purchase. Paying does not grant Bronze or bypass host/model admission. A
successful payment without successful model usage is a failed end-to-end test.

The contributor can report the stages completed today. Scheduling controlled
test leases, recording Bronze verdicts, and reconciling payment evidence remain
server-owned work; the public issue form does not enable those operations.
Real-money testing is optional and needs a separately configured program with
clear participant consent, price/spend cap and refund terms. Test credits do
not demonstrate a provider transfer or a bank payout.

## Share only the evidence needed

The public issue should contain a reviewed summary: release identity, exact
hardware/software tuple, public model/runtime pins, attempted stage, first
failure and measured results. Do not post enrollment tokens, private keys,
serials, MAC/IP addresses, account/payment references, private prompts, raw
service environments or private model paths. Sensitive evidence belongs in an
authenticated private support channel when available. An issue is not permission
to remotely administer your machine or copy model data.

Maintainers record the next action on the issue: needs information, reproduced,
implementation PR, awaiting signed test release, awaiting retest, or closed
with the release and outcome. Passing bring-up, passing Bronze, completing a
test purchase and production eligibility are separate results. A changed
board/driver/runtime/model/context requires the relevant tests again.
