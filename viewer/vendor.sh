#!/bin/sh
# Rebuilds embedpdf and ooxml from pinned npm packages. Run it with:
#   nix develop -c go generate ./viewer
set -eu

cd "$(dirname "$0")"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Downloads a package, checks it against npm's integrity hash and unpacks it into node_modules.
fetch() {
	name=$1
	version=$2
	integrity=$3
	tarball="$work/package.tgz"

	curl -fsSL -o "$tarball" "https://registry.npmjs.org/$name/-/${name##*/}-$version.tgz"

	expected=$(printf '%s' "${integrity#sha512-}" | base64 -d | od -An -v -tx1 | tr -d ' \n')
	actual=$(sha512sum "$tarball" | cut -d ' ' -f 1)

	if [ "$expected" != "$actual" ]; then
		echo "$name $version doesn't match its integrity hash" >&2
		exit 1
	fi

	mkdir -p "$work/node_modules/$name"
	tar -xzf "$tarball" -C "$work/node_modules/$name" --strip-components 1
}

fetch @silurus/ooxml 0.90.1 sha512-PXcySCSlW81X0omQMs8rsxScegEj8Ml1VA6raFkXEIbIHxlRXj3yRbCz5l+WXpjW2nWmXVUbZJTp+wapEssPaw==
fetch @embedpdf/engine 3.3.0 sha512-tLccuWhvXZyfOwLyNFk6E1KfwqNjlNL9bmiLyaoSPromfvoTr4qtuNGXOSeF4eQEj1rT+mtRx71304D5z8ewwg==
fetch @embedpdf/engine-core 3.3.0 sha512-3IPqht7g9vQG16+DUrhmnJIZlQTqk7b/jqmd/a2x0cvKbSUpyAZ9b44UsKgM31bQlzuG92ZZhg6ReumyJyUH3A==
fetch @embedpdf/engine-services 3.3.0 sha512-736m/p3gVjSx/vtHFptJhOYLOc/HA68NDIThZuDdLfLSriANVqNNWt75AaiHFT8OAFXEcOQoFXw7xbZC8/WWbw==
fetch @embedpdf/engine-runtime 3.3.0 sha512-YI4vYYrKSLfl0rbFszAGJ/SyLeXGIjX5fE0GbQH0Zvx6RkjW41NBlO2JK/W300bXSAhP8pBiifGoDoRns7fr4g==
fetch @embedpdf/engine-runtime-wasm32 3.3.0 sha512-mzDdTr/JJaHhMluxiogiC5uBX06Kfl2yIcBCLiBjIBAXj+95Telahiz84x+5fue7bfzr+peVkMJLysG0AUgt/g==
fetch @embedpdf/core-fonts 3.3.0 sha512-MAfd+OYXsaKQVhqIpoPufZyOKTxRRW+rYDF5ZMKJfi+wHS7+5MsoDYgjl+/TRiOYTruIlx7u34TubKwHuOHhNA==
fetch @embedpdf/default-fallback-fonts 1.0.1 sha512-Z9qeQ2mfjyhjmhLG4+LtTa/78LepJf8uVsE3d2vs741HKOi4/Ki8jyTEcRM/gPdPeCz98d9dXV6428C81A718Q==
fetch zod 3.25.76 sha512-gzUt/qt81nXsFGKIFcC3YnfEAx5NkunCfnDlvuBSSFS02bcXu4Lmea0AFIUwbLWxWPx3d9p8S5QoaujKcNQxcQ==
fetch detect-libc 2.1.2 sha512-Btj2BOOO83o3WyH59e8MgXsxEQVcarkUOpEYrubB0urwnN10yQ364rsiByU11nZlqWYZm05i/of7io4mzihBtQ==

modules="$work/node_modules"

# ooxml ships browser modules that load as they are. The type declarations and the MathJax fonts are never loaded.
rm -rf ooxml
mkdir ooxml
cp -R "$modules/@silurus/ooxml/dist/." ooxml/
rm -rf ooxml/types ooxml/mathjax-stix2.js
cp "$modules/@silurus/ooxml/LICENSE" "$modules/@silurus/ooxml/THIRD_PARTY_NOTICES.md" "$modules/@silurus/ooxml/package.json" ooxml/

# EmbedPDF imports its packages by name, so esbuild bundles them. The viewer names the worker file, so the inline worker chunks are never loaded.
echo 'export { localEngine } from "@embedpdf/engine";' > "$work/embedpdf.js"
(cd "$work" && go run github.com/evanw/esbuild/cmd/esbuild@v0.28.2 embedpdf.js --bundle --splitting --format=esm --platform=browser --minify --legal-comments=none --outdir=bundle --log-level=warning)
rm "$work"/bundle/embedpdf-worker.source-*.js "$work"/bundle/inline-transport-*.js

rm -rf embedpdf
mkdir -p embedpdf/licenses
cp "$work"/bundle/*.js embedpdf/
cp "$modules/@embedpdf/engine/workers/embedpdf-worker.js" "$modules/@embedpdf/engine-runtime-wasm32/lib/embedpdf.wasm" "$modules/@embedpdf/engine/package.json" embedpdf/
cp "$modules/@embedpdf/engine/LICENSE" embedpdf/licenses/embedpdf-LICENSE
cp "$modules/@embedpdf/engine-runtime-wasm32/LICENSE" embedpdf/licenses/pdfium-runtime-LICENSE
cp "$modules/@embedpdf/engine-runtime-wasm32/NOTICE.md" embedpdf/licenses/pdfium-runtime-NOTICE.md
cp "$modules/@embedpdf/core-fonts/THIRD_PARTY_NOTICES" embedpdf/licenses/core-fonts-THIRD_PARTY_NOTICES
cp -R "$modules/@embedpdf/default-fallback-fonts/licenses" embedpdf/licenses/fallback-fonts
cp "$modules/@embedpdf/default-fallback-fonts/NOTICE.md" embedpdf/licenses/fallback-fonts-NOTICE.md
cp "$modules/zod/LICENSE" embedpdf/licenses/zod-LICENSE
