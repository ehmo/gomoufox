class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.1"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.1/gomoufox_0.1.1_darwin_arm64.tar.gz"
      sha256 "f1cef6599e50fb6ca55c222c12c5c823d4f21ba49f290008fff7521413dce5d6"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.1/gomoufox_0.1.1_darwin_amd64.tar.gz"
      sha256 "7e2522e574800154979a86d13789abb5522f8d613a986bbff62d7a75f8214906"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.1/gomoufox_0.1.1_linux_arm64.tar.gz"
      sha256 "e209d3c84ccb6b544f2b97a8706dfa971fe5bfb2c1ebc8c038994c4ba53df329"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.1/gomoufox_0.1.1_linux_amd64.tar.gz"
      sha256 "caca74f1b8893cb31bff336b26b40642f44add70cb5bcd24856310962c3d10a9"
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
