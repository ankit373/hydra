# To publish this formula as a tap:
#   1. Create a repo named `homebrew-hydra` under your GitHub account
#   2. Copy this file to that repo's root as `Formula/hydra.rb`
#   3. Users install with:  brew tap ankit373/hydra && brew install hydra
#
# To release a proper versioned formula (replace HEAD):
#   1. Tag a release:  git tag v1.0.0 && git push origin v1.0.0
#   2. Download the tarball URL from GitHub releases
#   3. Run:  sha256sum hydra-v1.0.0.tar.gz
#   4. Replace the `head` block below with:
#        url "https://github.com/ankit373/hydra/archive/refs/tags/v1.0.0.tar.gz"
#        sha256 "<paste sha256 here>"

class Hydra < Formula
  desc "Multi-model AI orchestration TUI — routes tasks across Claude, Gemini, GPT, and local models"
  homepage "https://github.com/ankit373/hydra"
  license "MIT"

  head "https://github.com/ankit373/hydra.git", branch: "main"

  depends_on "jq"
  depends_on "yq"
  depends_on "oven-sh/bun/bun"

  def install
    # Install scripts and registry (read-only, shared)
    libexec.install "dispatch", "registry", "context", "skills", "ui"

    # Make all dispatch scripts executable
    Dir["#{libexec}/dispatch/*.sh"].each { |f| chmod 0755, f }

    # Install UI npm/bun dependencies
    cd libexec/"ui" do
      system "bun", "install", "--frozen-lockfile"
    end

    # Write the `hydra` bin wrapper
    # HYDRA_HOME → libexec (read-only scripts, set at install time)
    # HYDRA_DATA → ~/.hydra (mutable: logs, state — always user-owned)
    (bin/"hydra").write <<~SH
      #!/usr/bin/env bash
      set -euo pipefail
      export HYDRA_HOME="#{libexec}"
      export HYDRA_DATA="${HYDRA_DATA:-$HOME/.hydra}"
      mkdir -p "$HYDRA_DATA/logs"
      if [[ ! -f "$HYDRA_DATA/logs/state.json" ]]; then
        echo '{"claude_pct":0,"exhausted_pools":[]}' > "$HYDRA_DATA/logs/state.json"
      fi
      exec bun "#{libexec}/ui/src/index.tsx" "$@"
    SH
    chmod 0755, bin/"hydra"
  end

  def caveats
    <<~EOS
      Hydra requires two external tools not available via Homebrew:

        agy  (Antigravity CLI / Windsurf)  — https://windsurf.com
        agy must be authenticated before use: run `agy` interactively once.

      Ollama is optional (used for Tier 10 local inference):
        brew install ollama

      Your mutable data (logs, state) lives in:
        ~/.hydra/

      To override: export HYDRA_DATA=/your/path before running hydra.
    EOS
  end

  test do
    # Verify the wrapper is executable and HYDRA_HOME resolves correctly
    ENV["HYDRA_DATA"] = testpath.to_s
    (testpath/"logs").mkpath
    (testpath/"logs/state.json").write('{"claude_pct":0,"exhausted_pools":[]}')
    assert_predicate bin/"hydra", :executable?
    assert_match "dispatch/route.sh", shell_output("cat #{bin}/hydra")
  end
end
