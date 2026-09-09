class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.29"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.29/gomoufox_0.1.29_darwin_arm64.tar.gz"
      sha256 "53654d24b3d2633791dc6b834d40c4392d056322cdaad783fa5bbb02e95016d4"
    else
      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.29/gomoufox_0.1.29_linux_amd64.tar.gz"
      sha256 "5a667b034e99909046c4efd90e1b97f3a66989dce4eff2f839c08c0b79f05e35"
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
