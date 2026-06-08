class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.4"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.4/gomoufox_0.1.4_darwin_arm64.tar.gz"
      sha256 "32d6c60f750963119042d3d587eaceb0ee7bc14030fdc75acbccf95e054cdc66"
    else
      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.4/gomoufox_0.1.4_linux_amd64.tar.gz"
      sha256 "96cb777bcb5c2028be85f9b6e9b8fe6c7ec40ec2cad328e1e1503c3a90d8b922"
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
