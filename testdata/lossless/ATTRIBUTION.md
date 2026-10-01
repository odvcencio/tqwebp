# Fixture provenance and image credits

The pinned copies are from golang.org/x/image v0.38.0/testdata. Their encoded
bytes and independent libwebp 1.5.0 RGBA outputs are hashed in provenance.json.
The Go Authors' BSD notice is retained in LICENSE.x-image for source materials.
Image artwork has its own provenance, not an assumed code license:

- gopher-doc.*: Go gopher artwork by Renee French. The original Go article
  identifies Creative Commons Attribution 3.0; current Go documentation identifies
  CC BY 4.0. Attribution and license references:
  https://go.googlesource.com/blog/+/3c9e99d94ae226f9fbcb3e4e9de7a46af33150f2%5E%21/
  https://creativecommons.org/licenses/by/3.0/
  https://go.dev/blog/gopher
  https://creativecommons.org/licenses/by/4.0/
  These are upstream palette/alpha codec-test variants. This candidate retains
  their WebP bytes and adds unmodified decoded-channel oracle representations.
- yellow_rose.*: Jon Sullivan, public-domain Yellow Rose image, credited by
  the official WebP lossless/alpha gallery:
  https://developers.google.com/speed/webp/gallery2#image-credits
- tux.lossless.webp: Fizyplankton's public-domain baby tux image, credited by
  the same official gallery. This is not an assumed license for other Tux art.
- blue-purple-pink*: pinned Go Authors test fixtures; BSD notice retained.
- lossless-pattern*, lossless-boundary*, compressed-alpha-filter-*, hidden-rgb and duplicate-simple-alignment: newly generated
  procedural/specification fixtures by this project, under its MIT license.
  The generator records the explicit libwebp development oracle and mode.

Raw RGBA oracle files are numeric decoded samples of their corresponding
fixtures and carry the same artwork attribution. No image is used as branding
or endorsement. No third-party image URL is fetched by the production library.
