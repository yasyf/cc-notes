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
  version "0.7.0"
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
      sha256 "f6c0b2dca572308523fa96bbdbe0c749fd750bde64e48db6ad5a6063da94377c" # darwin-arm64
    end
    on_intel do
      url "https://github.com/yasyf/cc-notes/releases/download/v#{version}/cc-notes_darwin_amd64_fuse"
      sha256 "a231ec16c1a4d12997929569564de4660b1afa0d8ba64bc8c925c2f250aa4591" # darwin-amd64
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/yasyf/cc-notes/releases/download/v#{version}/cc-notes_linux_amd64_fuse"
      sha256 "bfa4590822e8dfa785700c955333ee68226511f4ef0e4d08beffceec2c9b7dca" # linux-amd64
    end
    on_arm do
      # No FUSE variant ships for linux/arm64; this is the pure binary.
      url "https://github.com/yasyf/cc-notes/releases/download/v#{version}/cc-notes_linux_arm64"
      sha256 "2503aaaa65eddb6399120cbdaa703a9e47ba1e026891f558659cd7f930682327" # linux-arm64
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
