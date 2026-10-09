{
  config,
  lib,
  pkgs,
  ...
}:

let
  readVersionFile = path: lib.removeSuffix "\n" (builtins.readFile path);

  parseToolVersion = line:
    let
      match = builtins.match "([A-Z0-9_]+)=(v?[0-9].*)" line;
    in
    if match == null then
      null
    else
      {
        name = builtins.elemAt match 0;
        value = builtins.elemAt match 1;
      };
  toolVersions = builtins.listToAttrs (
    builtins.filter (entry: entry != null) (
      map parseToolVersion (lib.splitString "\n" (builtins.readFile ./hack/dev/tool-versions.env))
    )
  );
  toolVersion = name:
    if builtins.hasAttr name toolVersions then
      lib.removePrefix "v" toolVersions.${name}
    else
      throw "hack/dev/tool-versions.env does not declare ${name}";

  goLine =
    lib.findFirst (line: lib.hasPrefix "go " line) (throw "go.mod does not declare a Go version")
      (lib.splitString "\n" (builtins.readFile ./go.mod));
  goVersion = lib.removePrefix "go " goLine;
  hugoVersion = readVersionFile ./.hugo-version;

  exactPackage =
    name: expected: package:
    if lib.getVersion package == expected then
      package
    else
      throw "${name} version mismatch: expected ${expected}, nixpkgs provides ${lib.getVersion package}";

  goPackage = exactPackage "Go" goVersion (
    pkgs.go_1_27.overrideAttrs {
      version = "1.27.2";
      src = pkgs.fetchurl {
        url = "https://go.dev/dl/go1.27.2.src.tar.gz";
        hash = "sha256-A0ldorpkiU1A9cSZLklFT6eLUGkGBP+Stq//UIG3bmI=";
      };
    }
  );
  spdxSchema22 = pkgs.fetchurl {
    url = "https://raw.githubusercontent.com/spdx/spdx-spec/a05c12a2dd4652b1396fd2659f2cd3ea1f37faba/schemas/spdx-schema.json";
    hash = "sha256-yDKNFMM2Iaa+kXVprUwyPTcCIEEu263cN8zx6T48qIo=";
  };
  spdxSchema23 = pkgs.fetchurl {
    url = "https://raw.githubusercontent.com/spdx/spdx-spec/aadf3b0b8dbbabdb4d880b0fc714255fea436ff7/schemas/spdx-schema.json";
    hash = "sha256-I5IIt6woezz12amvI/nWmGOXEQKl4Vh6J6OYtDSQuJs=";
  };
  spdxFixture22 = pkgs.fetchurl {
    url = "https://raw.githubusercontent.com/spdx/spdx-spec/a05c12a2dd4652b1396fd2659f2cd3ea1f37faba/examples/SPDXJSONExample-v2.2.spdx.json";
    hash = "sha256-4FusyQ1y2fUwu+aLdspor9duoLBzuUXHvJdfUiBRrOw=";
  };
  spdxFixture23 = pkgs.fetchurl {
    url = "https://raw.githubusercontent.com/anchore/syft/2293641e3bd628a01bb37639318d62c0ebe89b39/syft/format/spdxjson/testdata/identify/2.3.json";
    hash = "sha256-OaBZ+Nf1kvK7H1DQrjH1aKlEcUBdyLEq+OOMCQSsvAU=";
  };

in
{
  name = "openbao-operator";

  packages = [
    pkgs.bash
    pkgs.check-jsonschema
    pkgs.coreutils
    pkgs.curl
    pkgs.docker-client
    pkgs.findutils
    pkgs.gawk
    pkgs.git
    pkgs.gnugrep
    pkgs.gnused
    goPackage
    pkgs.jq
    (exactPackage "Kind" (toolVersion "KIND_VERSION") pkgs.kind)
    (exactPackage "kubectl" (toolVersion "KUBECTL_VERSION") pkgs.kubectl)
    pkgs.gnumake
    pkgs.python3
    (exactPackage "Tilt" (toolVersion "TILT_VERSION") pkgs.tilt)
    (exactPackage "Trivy" (toolVersion "TRIVY_VERSION") pkgs.trivy)
    (exactPackage "Helm" (toolVersion "HELM_VERSION") pkgs.kubernetes-helm)
    (exactPackage "Hugo" hugoVersion pkgs.hugo)
  ];

  env = {
    # Match the Go language module's normalized value when the editor profile is active.
    GOROOT = "${goPackage}/share/go/";
    GOTOOLCHAIN = "local";
    SPDX_SCHEMA_2_2 = "${spdxSchema22}";
    SPDX_SCHEMA_2_3 = "${spdxSchema23}";
    SPDX_FIXTURE_2_2 = "${spdxFixture22}";
    SPDX_FIXTURE_2_3 = "${spdxFixture23}";
  };

  profiles.editor.module = {
    overlays = [
      (_: previous: {
        go-tools = previous.go-tools.overrideAttrs {
          version = "2026.2.1";
          src = previous.fetchFromGitHub {
            owner = "dominikh";
            repo = "go-tools";
            tag = "2026.2.1";
            hash = "sha256-wellofnfLW4lQy68UQyFJfvrKCfrZ/EllLODX1g9taY=";
          };
          vendorHash = "sha256-3no4wPqFG0RfSsWB0z8EYxeoZ30t+Zf7ZayzFCLEm2A=";
        };
      })
    ];
    languages.go = {
      enable = true;
      package = goPackage;
      delve.enable = true;
      lsp.enable = true;
    };
  };

  enterShell = ''
    export GOPATH="''${XDG_CACHE_HOME:-$HOME/.cache}/openbao-operator/go"
    export PATH="$DEVENV_PROFILE/bin:$DEVENV_ROOT/bin:$GOPATH/bin:$PATH"
  '';

  git-hooks.hooks = {
    openbao-operator-pre-commit = {
      enable = true;
      name = "OpenBao Operator pre-commit";
      entry = "${pkgs.bash}/bin/bash hack/dev/pre-commit.sh";
      stages = [ "pre-commit" ];
      pass_filenames = false;
      always_run = true;
    };

    openbao-operator-pre-push = {
      enable = true;
      name = "OpenBao Operator pre-push";
      entry = "${pkgs.bash}/bin/bash hack/dev/pre-push.sh";
      stages = [ "pre-push" ];
      pass_filenames = false;
      always_run = true;
    };
  };

  tasks = {
    "operator:migrate-legacy-git-hooks" = {
      description = "Remove the retired .githooks path before Devenv installs native hooks";
      exec = ''
        if test "$(git config --local --get core.hooksPath 2>/dev/null || true)" = ".githooks"; then
          git config --local --unset-all core.hooksPath
        fi
      '';
      status = ''
        test "$(git config --local --get core.hooksPath 2>/dev/null || true)" != ".githooks"
      '';
      cwd = config.git.root;
      before = [ "devenv:git-hooks:install" ];
    };

    "operator:verify-toolchain" = {
      description = "Verify the pinned service-independent toolchain contract";
      exec = "make verify-devenv";
      cwd = config.git.root;
      after = [ "devenv:git-hooks:install" ];
      before = [ "devenv:enterTest" ];
    };

    "operator:verify-spdx-normalizer" = {
      description = "Validate deterministic SPDX normalization against SPDX 2.2 and 2.3";
      exec = "make verify-spdx-normalizer";
      cwd = config.git.root;
      after = [ "operator:verify-toolchain" ];
      before = [ "devenv:enterTest" ];
    };

    "operator:bootstrap" = {
      description = "Install repository-managed tools required by the core contributor workflow";
      exec = "make bootstrap";
      cwd = config.git.root;
    };

    "operator:doctor" = {
      description = "Check external runtime prerequisites such as Docker and Kubernetes access";
      exec = "make doctor";
      cwd = config.git.root;
    };

    "operator:ci-core" = {
      description = "Run the cluster-independent pull-request-equivalent gate";
      exec = "make ci-core";
      cwd = config.git.root;
    };
  };
}
