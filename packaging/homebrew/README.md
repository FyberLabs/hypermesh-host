# Homebrew adapter

**Binary:** `hypermesh-host`
**Code home:** [FyberLabs/hypermesh-host](https://github.com/FyberLabs/hypermesh-host)
**Formula:** [`hypermesh-host.rb`](hypermesh-host.rb) — formula, not cask

Linux on the AGX is the Go binary plus the existing systemd units. This directory is the operator adapter until a tap exists.

## What the tap still needs

There is no `fyber/homebrew-tap` or `FyberLabs/homebrew-tap` in this workspace. When that tap exists:

1. Copy [`hypermesh-host.rb`](hypermesh-host.rb) to `Formula/hypermesh-host.rb` in the tap.
2. Keep this file as the review source. Do not grow a second formula.
3. Cut a tagged release before bottles. Until then the formula is HEAD-only (`brew install --HEAD`).
4. GoReleaser already builds linux amd64 and arm64 archives (`.goreleaser.yaml`). The tap does not have to be the Linux install path.

`go install` stays the dev path:

```bash
go install github.com/FyberLabs/hypermesh-host/cmd/hypermesh-host@latest
```

## Install from this checkout

```bash
brew install --HEAD --formula ./packaging/homebrew/hypermesh-host.rb
hypermesh-host agx --help
```

## Install from a private tap (when it exists)

```bash
brew tap fyber/homebrew-tap
brew install --HEAD hypermesh-host
```

The older hypermesh-deploy formula remains available as a compatibility command.
