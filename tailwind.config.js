/** @type {import('tailwindcss').Config} */
// Tailwind build for the Go web UI. The compiled output is committed at
// internal/web/static/app.css and embedded into the binary (see routes.go), so
// `go run` / `go build` need no Node toolchain — only regenerating the CSS does.
module.exports = {
  darkMode: "class",
  content: ["./internal/web/templates/**/*.html"],
  theme: {
    extend: {
      fontFamily: {
        sans: ["Inter", "system-ui", "sans-serif"],
        mono: ["JetBrains Mono", "monospace"],
      },
    },
  },
  plugins: [],
};
