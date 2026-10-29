class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.33"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.33/gomoufox_0.1.33_darwin_arm64.tar.gz"
      sha256 "79d2e122e75db1f7a1f731f46d05014a763214bf62b935801291f2360a9487ca"
    else
      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.33/gomoufox_0.1.33_linux_amd64.tar.gz"
      sha256 "7b0f564a375cc529409d9e7699a89742a2f9e9cd9d5bca2725cca194b9594772"
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
