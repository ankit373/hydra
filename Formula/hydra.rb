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
  desc "AI control plane — routes prompts across Claude, Gemini, GPT, and local models with cost and policy controls"
  homepage "https://hydra.uvansa.com"
  license "MIT"

  head "https://github.com/ankit373/hydra.git", branch: "main"

  depends_on "go" => :build
  depends_on "jq"
  depends_on "yq"
  depends_on "oven-sh/bun/bun"

  def install
    # Install registry (YAML config) + UI (read-only, shared)
    libexec.install "registry", "context", "skills", "ui"

    # Build the Go control plane binary into libexec so the bin/ wrapper can call it
    system "go", "build", "-o", libexec/"hydra", "./cmd/hydra"
    chmod 0755, libexec/"hydra"

    # Install UI npm/bun dependencies
    cd libexec/"ui" do
      system "bun", "install", "--frozen-lockfile"
    end

    # hydra → Go binary wrapper
    # Sets HYDRA_HOME so the binary finds registry/models.yaml.
    (bin/"hydra").write <<~SH
      #!/usr/bin/env bash
      set -euo pipefail
      export HYDRA_HOME="#{libexec}"
      export HYDRA_DATA="${HYDRA_DATA:-$HOME/.hydra}"
      mkdir -p "$HYDRA_DATA/logs"
      if [[ ! -f "$HYDRA_DATA/logs/state.json" ]]; then
        echo '{"claude_pct":0,"exhausted_pools":[]}' > "$HYDRA_DATA/logs/state.json"
      fi
      exec "#{libexec}/hydra" "$@"
    SH
    chmod 0755, bin/"hydra"

    # hydra-ui → Ink TUI dashboard
    (bin/"hydra-ui").write <<~SH
      #!/usr/bin/env bash
      set -euo pipefail
      export HYDRA_HOME="#{libexec}"
      export HYDRA_DATA="${HYDRA_DATA:-$HOME/.hydra}"
      mkdir -p "$HYDRA_DATA/logs"
      if [[ ! -f "$HYDRA_DATA/logs/state.json" ]]; then
        echo '{"claude_pct":0,"exhausted_pools":[]}' > "$HYDRA_DATA/logs/state.json"
      fi
      exec env NODE_ENV=production bun "#{libexec}/ui/src/index.tsx" "$@"
    SH
    chmod 0755, bin/"hydra-ui"
  end

  def caveats
    <<~EOS
      Hydra requires two external tools not available via Homebrew:

        agy  (Antigravity CLI / Windsurf)  — https://windsurf.com
        agy must be authenticated before use: run `agy` interactively once.

      Ollama is optional (used for local inference):
        brew install ollama

      Your mutable data (logs, state) lives in:
        ~/.hydra/   (override with HYDRA_DATA=/your/path)

      Commands:
        hydra dispatch --help   — Go control plane
        hydra-ui                — Ink TUI monitoring dashboard
    EOS
  end

  test do
    ENV["HYDRA_DATA"] = testpath.to_s
    (testpath/"logs").mkpath
    (testpath/"logs/state.json").write('{"claude_pct":0,"exhausted_pools":[]}')
    assert_predicate bin/"hydra", :executable?
    assert_predicate bin/"hydra-ui", :executable?
    assert_match "HYDRA_HOME", shell_output("cat #{bin}/hydra")
    assert_match "libexec/hydra", shell_output("cat #{bin}/hydra")
  end
end
