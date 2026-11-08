package fingerprint

import "testing"

func TestIs(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Bundler output.
		{"/assets/index-F6vy2umU.js", true},           // Vite
		{"/assets/index-B3x_9-aZ.css", true},          // Vite, base64url with _ and -
		{"/assets/vendor.3f9a1c2b.js", true},          // Parcel / webpack [contenthash:8]
		{"/static/js/main.3f9a1c2b7d4e5f60.js", true}, // CRA, 16 hex
		{"/_next/static/chunks/main-a1b2c3d4e5f6a7b8c9d0.js", true},
		{"/_app/immutable/entry/start.Ab12Cd34.js", true}, // SvelteKit
		{"/assets/fonts/inter-Q1w2E3r4.woff2", true},
		{"/play/assets/game-9fA2kLm0.wasm", true}, // nested app folder
		{"/assets/chunk-ABCD1234.js", true},       // esbuild

		// Not fingerprinted.
		{"/index.html", false},
		{"/assets/logo.png", false},
		{"/assets/fonts/poppins-semibold.ttf", false}, // 8 letters, no digit
		{"/assets/report-20241101.pdf", false},        // 8 digits, no letter
		{"/assets/image-1920x1080.jpg", false},        // 9 chars
		{"/beach-IMG_1234.jpg", false},                // looks hashed, not in a bundle dir
		{"/photos/beach-IMG_1234.jpg", false},
		{"/assets/index-F6vy2umU", false},           // no extension
		{"/assets/index-F6vy2umU.", false},          // empty extension
		{"/assets/F6vy2umU.js", false},              // no separator
		{"/assets/main.3F9A1C2B7D4E5F60.js", false}, // uppercase hex isn't webpack's
		{"/assets/main.3f9a1c2b7d4e.js", false},     // 12 hex: neither form
		{"/assets/x-F6vy2umU.js.map/", false},       // a folder
		{"", false},
		{"/", false},
	}
	for _, c := range cases {
		if got := Is(c.path); got != c.want {
			t.Errorf("Is(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
