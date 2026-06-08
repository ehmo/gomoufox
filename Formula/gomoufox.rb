class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.2"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.2/gomoufox_0.1.2_darwin_arm64.tar.gz"
      sha256 "ed7024b7674bc7bc1b31b40a7d5c9413e3c28555e8d97c477f89c856a76177ea"
    else
      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.2/gomoufox_0.1.2_linux_amd64.tar.gz"
      sha256 "18a2394051706c9f0084d2017f5c1e55cbd27d82b4903e232e95d5de9ec60631"
    end
  end

  def install
    bin.install "gomoufox"
    bin.install "gomoufox-realpass"
  end

  test do
    assert_match "gomoufox v#{version}", shell_output("#{bin}/gomoufox --version")
    assert_match "gomoufox-realpass v#{version}", shell_output("#{bin}/gomoufox-realpass --version")
    assert_match "commands", shell_output("#{bin}/gomoufox help --json --fields commands")
  end
end
