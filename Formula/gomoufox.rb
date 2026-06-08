class Gomoufox < Formula
  desc "Go driver, CLI, and MCP server for Camoufox"
  homepage "https://github.com/ehmo/gomoufox"
  version "0.1.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.0/gomoufox_0.1.0_darwin_arm64.tar.gz"
      sha256 "a81d416c19fe531517a4647945b423d3da55c0d22846ef0c044037ed6a72661b"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.0/gomoufox_0.1.0_darwin_amd64.tar.gz"
      sha256 "53ad48b2f7b5d718e7507dac86908a1422bf7324b2785a244d4c150f17b7f004"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.0/gomoufox_0.1.0_linux_arm64.tar.gz"
      sha256 "34365ac258b5270d5e6d4d740857e6fb84af7b6f98a75ce5cfdb7bd86d1917ca"
    else
      url "https://github.com/ehmo/gomoufox/releases/download/v0.1.0/gomoufox_0.1.0_linux_amd64.tar.gz"
      sha256 "e154a4494049e5c70c71e8afedba50b68b40cd31f821c887810d7a17dd3ceafb"
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
