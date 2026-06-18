# Homebrew formula for cc-notes. Installs the prebuilt binary for the current
# platform from GitHub Releases — no Go toolchain needed. `brew install
# --HEAD` builds (pure Go) from source instead.
#
#   brew tap yasyf/cc-notes https://github.com/yasyf/cc-notes
#   brew install yasyf/cc-notes/cc-notes
#
# The FUSE-capable build ships everywhere one is published (macOS both arches,
# linux/amd64) and runs fine without FUSE present — only `cc-notes mount`
# needs fuse-t (macOS) or fuse3 (Linux) at runtime. If asset names or the
# fuse matrix change, scripts/install.sh and the bump-formula seds in
# .github/workflows/release.yml must change in lockstep.
#
# release.yml's bump-formula job rewrites the version line and the four
# sha256 lines on every stable tag — the trailing `# darwin-arm64` etc.
# markers anchor its seds; keep them.
class CcNotes < Formula
  desc "Git-native notes and tasks layer for agents"
  homepage "https://github.com/yasyf/cc-notes"
  version "0.5.0"
  license "PolyForm-Noncommercial-1.0.0"

  livecheck do
    url :stable
    strategy :github_latest
  end

  head do
    url "https://github.com/yasyf/cc-notes.git", branch: "main"
    depends_on "go" => :build
  end

  on_macos do
    on_arm do
      url "https://github.com/yasyf/cc-notes/releases/download/v#{version}/cc-notes_darwin_arm64_fuse"
      sha256 "0f524928acdafb4571ca84291f508b7e7fd3c8f3b2a61ccdf748d50ae670f805" # darwin-arm64
    end
    on_intel do
      url "https://github.com/yasyf/cc-notes/releases/download/v#{version}/cc-notes_darwin_amd64_fuse"
      sha256 "6e6c4da99b0679ca4d9700fc9f2a23e3764686784aac5321e664be7d0e163bf1" # darwin-amd64
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/yasyf/cc-notes/releases/download/v#{version}/cc-notes_linux_amd64_fuse"
      sha256 "ce463268f7100a8b3cc3086154b83d2be201053b5c8ddc4f2ca96c309c3d3bdd" # linux-amd64
    end
    on_arm do
      # No FUSE variant ships for linux/arm64; this is the pure binary.
      url "https://github.com/yasyf/cc-notes/releases/download/v#{version}/cc-notes_linux_arm64"
      sha256 "20db81e81eb2036a68d4522c215552513ae53cbeb776b75b0844a89adc115d91" # linux-arm64
    end
  end

  def install
    if build.head?
      ENV["CGO_ENABLED"] = "0"
      ldflags = "-s -w -X github.com/yasyf/cc-notes/internal/version.Version=#{version}"
      system "go", "build", *std_go_args(ldflags: ldflags, output: bin/"cc-notes"), "./cmd/cc-notes"
    else
      # The release asset is a bare binary staged under its asset name.
      bin.install Dir["cc-notes_*"].first => "cc-notes"
    end
  end

  def caveats
    on_macos do
      <<~EOS
        `cc-notes mount` needs fuse-t at runtime:
          brew install macos-fuse-t/cask/fuse-t
        Everything else works without it.
      EOS
    end
  end

  test do
    # Release binaries print "<tag> (<commit>)", e.g. "v0.2.0 (ab12cd3)".
    assert_match version.to_s, shell_output("#{bin}/cc-notes version")
  end
end
