class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.6"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.6/gomoufox_0.1.6_darwin_arm64.tar.gz"
      sha256 "bbc385a87a987093aa28d098e90f37e5b7a1b4ec889b694dbd81a5c66ebafb3d"
    else
      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.6/gomoufox_0.1.6_linux_amd64.tar.gz"
      sha256 "304688587eb4c79b0000a44a2fff5ff1a63a65b371322fae845944f864d75736"
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
