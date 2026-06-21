class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.16"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.16/gomoufox_0.1.16_darwin_arm64.tar.gz"
      sha256 "3b51d70808600cb28eee84625cbfb34f115a25b0586528617df15efb168701e9"
    else
      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.16/gomoufox_0.1.16_linux_amd64.tar.gz"
      sha256 "f9f388a1f53d8f3b1d37946bc768263c35d1cd3559d0832eb45ef335bf31afdb"
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
