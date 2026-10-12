class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.30"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.30/gomoufox_0.1.30_darwin_arm64.tar.gz"
      sha256 "089e593fd43aa4d8afa9ff706a7cc54df448ba21ff37b5fc5815a8e64e42f6bf"
    else
      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.30/gomoufox_0.1.30_linux_amd64.tar.gz"
      sha256 "a1671f3e8019d5ed83426f7bce79bf17d685a907b7d7e1f0a50a5c8f41f7a83b"
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
