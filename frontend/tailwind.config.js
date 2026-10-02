/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      // Палитра панели: тёмная земля, одна акцентная и три статусных
      colors: {
        ground: '#0E1014',
        side: '#12151B',
        surf: '#171B22',
        surf2: '#1D222B',
        line: '#272D38',
        fg: '#E7EAF0',
        mute: '#A0A8B8',
        dim: '#7C8596',
        acc: { DEFAULT: '#6AA8FF', bg: '#1A2A44', hover: '#8BBBFF' },
        ok: { DEFAULT: '#4CC38A', bg: '#15301F' },
        warn: { DEFAULT: '#F0B04A', bg: '#352A14' },
        err: { DEFAULT: '#F07A6E', bg: '#3A1D1B', soft: '#F4B3AC' },
      },
      fontFamily: {
        sans: ['"IBM Plex Sans"', 'system-ui', 'sans-serif'],
        mono: ['"IBM Plex Mono"', 'ui-monospace', 'monospace'],
      },
    },
  },
  plugins: [],
}
