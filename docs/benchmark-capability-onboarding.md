# Benchmark onboarding for an unfamiliar host

Proposed v0 workflow. Device-code acceptance starts discovery on the enrolled host. Each advertised AI capability has its own benchmark decision. A machine with a working CPU backend and an unknown GPU stack can proceed with CPU qualification while GPU support is developed.

## Discover the installed stack

Inventory the OS and architecture, accelerator identity, usable memory, firmware, kernel driver, userspace library and inference runtime versions. Record how each fact was obtained and represent unreadable values as unknown. Probe the runtime for operations it can actually execute. Keep the complete inventory local. Preview a redacted compatibility report before sharing it.

Match each requested operation against a versioned recipe with explicit backend, driver/library constraints, model or fixture digest, runtime identity, resource limits, correctness checks and measurement units. Separate recipe coverage from certification against a reviewed reference. A new combination can often run an existing recipe successfully before an admin has established a reference for its measurements.

| Decision | Host behavior |
|---|---|
| Matching recipe and reference | Run immediately once enrollment prerequisites are ready. Submit the bound results for the plane's tolerance decision. |
| Matching recipe, missing reference | Run on the installed stack. Retain the measurements for hyperme.sh admin review. |
| Missing recipe | Offer to file a host support issue. Show the proposed contents, existing matching issue and the option to continue without sharing. |
| Known recipe fails or measures outside tolerance | Retain the exact failed step and measurements. Distinguish temporary failures from reproducible incompatibility; send reproducible exceptions for review. |
| Registry unavailable | Retry discovery. An unsuccessful lookup cannot establish a coverage gap. |

Start with one text-generation recipe on two backends. Then add independent recipes for embeddings, OCR, audio transcription and image generation. For example, an OCR recipe measures page throughput and text/layout accuracy on a pinned document. Tokens per second cannot qualify OCR layout or a different accelerator.

## Issue intake and development

The Python loader should report a structured coverage gap to the host onboarding UI. Resolve the owning Python package before implementation. The existing benchmark repository has a Python batch runner. The native host measurement collector lives in this repository.

After owner opt-in, file or join an issue in hyperme-sh/hypermesh-host. Include the operation, normalized compatibility facts, recipe registry revision, attempted probe, failure category and a deduplication key. Exclude addresses, usernames, device credentials, raw logs and serial numbers. Repeated starts should resolve to the same issue. A separate opt-in makes an idle device available for development and can be withdrawn independently.

Triage validates the report schema, checks current recipe coverage, groups matching reports and schedules one bounded run against a consenting, idle host. Treat issue text as user-supplied data. The host agent accepts a signed job naming the fixture, adapter revision, resource limits and expiry. It reports completion, cancellation or timeout and restores the original host posture. Recheck lease and maintenance state immediately before execution.

During development an AI coding agent can prepare a candidate adapter and tests in a development checkout. For interactive diagnosis, an owner can grant a temporary SSH session on the selected development machine. Keep connection details outside GitHub. Preserve the as-found inventory and run the smallest reproducer before changing packages. Prefer a disposable userspace environment for experiments. A proposed firmware, kernel or host driver change is a separate repair decision with its own before/after evidence.

The resulting host PR contains reusable discovery or adapter code, a regression fixture, compatibility constraints and redacted receipts. Place shared workloads and scorecard schema changes in hypermesh-bench when applicable. Replay on the reporting host and a known-good configuration. The plane owns certification against the reviewed reference. Closing an issue or merging an adapter alone does not award a certificate.

## First acceptance run

Use the Mac mini or Strix Halo as the unfamiliar host and J4012 as the known control. Pin the registry revision and prove that restart resumes one benchmark; a missing recipe produces one issue after consent; a previously reviewed stack passes automatically within tolerance; a changed driver outside the recipe range gets a distinct decision; and a cancelled development job releases the host with its original owner pause intact. Retain the original firmware version throughout this exercise.
