import { defineConfig, presetUno } from 'unocss'

export default defineConfig({
  presets: [presetUno()],
  theme: {
    colors: {
      ink: '#17202a',
      muted: '#5f6b7a',
      line: '#d8dee8',
      panel: '#f7f9fc',
      surface: '#ffffff',
      accent: '#1f7a8c',
      danger: '#b42318',
      warn: '#a15c07',
    },
  },
})
