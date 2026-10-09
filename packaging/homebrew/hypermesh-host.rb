# frozen_string_literal: true

# Hypermesh host CLI (formula, not cask).
# Source of truth until a private tap exists: this file in FyberLabs/hypermesh-host.
# Copy into fyber/homebrew-tap (or FyberLabs/homebrew-tap) Formula/hypermesh-host.rb
# when that tap is created. HEAD-only until a tagged release. No bottle.
# Dev install stays: go install github.com/FyberLabs/hypermesh-host/cmd/hypermesh-host@latest

class HypermeshHost < Formula
  desc "Hypermesh host setup and management"
  homepage "https://github.com/FyberLabs/hypermesh-host"
  head "https://github.com/FyberLabs/hypermesh-host.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", "-o", bin/"hypermesh-host", "./cmd/hypermesh-host"
  end

  def caveats
    <<~EOS
      This formula installs the operator CLI. The AGX enroll path is still
      hypermesh-agent plus hypermesh-wg on the box.

      -serial is required. The device id is taken from the plane
      device list for that serial. There is no device-id flag.

      Dev install, without Homebrew:
        go install github.com/FyberLabs/hypermesh-host/cmd/hypermesh-host@latest
    EOS
  end

  test do
    assert_match "Hypermesh", shell_output("#{bin}/hypermesh-host --help 2>&1")
  end
end
