{
  description = "A terminal email client with a browser viewer";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];

      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        rec {
          scry = pkgs.buildGo127Module {
            pname = "scry";

            version = builtins.head (
              builtins.match ".*const version = \"([^\"]+)\".*" (builtins.readFile ./main.go)
            );

            src = ./.;

            vendorHash = "sha256-ZdfuRHdOTG9lYs+qbMZmsNtUAVnyy+rW+2LKhwSMlF0=";

            env.CGO_ENABLED = 0;

            ldflags = [
              "-s"
              "-w"
            ];

            postInstall = ''
              install -Dm644 config.example.toml $out/share/doc/scry/config.example.toml
            '';

            meta = {
              description = "A terminal email client with a browser viewer";
              homepage = "https://github.com/siliconwitch/scry";
              license = nixpkgs.lib.licenses.mit;
              mainProgram = "scry";
              platforms = nixpkgs.lib.platforms.linux;
            };
          };

          default = scry;
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            packages = [
              pkgs.go_1_27
              pkgs.goreleaser
            ];

            shellHook = ''
              export CGO_ENABLED=0
            '';
          };
        }
      );
    };
}
