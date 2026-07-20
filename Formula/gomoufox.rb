class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.20"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.20/gomoufox_0.1.20_darwin_arm64.tar.gz"
      sha256 "5aba6b9f6a3e49b9202b5cd436a94750e922c507b3da02525486c74e26dcc4f5"
    else
      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.20/gomoufox_0.1.20_linux_amd64.tar.gz"
      sha256 "9dd8d020de7560052cce8b4381d20f7d91fcd7460d327446676a38749303c87c"
    end
  end

  def install
    archive_root = Dir["gomoufox_*"].find { |path| File.directory?(path) } || "."
    bin.install "#{archive_root}/gomoufox"
    bin.install "#{archive_root}/gomoufox-realpass"
  end

  test do
    assert_match "gomoufox v#{version}", shell_output("#{bin}/gomoufox --version")
    assert_match "gomoufox-realpass v#{version}", shell_output("#{bin}/gomoufox-realpass --version")
    assert_match "commands", shell_output("#{bin}/gomoufox help --json --fields commands")
    assert_match "actions", shell_output("#{bin}/gomoufox agents install --target all --scope user --features skills,mcp --toolset core --dry-run --json")
  end
end
